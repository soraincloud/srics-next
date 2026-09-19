// Package store implements the M0 immutable-object and SQLite snapshot proof.
// Public uploads and production migrations are deliberately not exposed yet.
package store

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"filippo.io/age"
	"github.com/mattn/go-sqlite3"
	"github.com/soraincloud/srics-next/internal/atomicfile"
	"github.com/soraincloud/srics-next/internal/vault"
)

const Format = "srics-next-m0-v1\n"
const maxFixtureBytes = 32 << 20

var validID = regexp.MustCompile(`^[a-f0-9]{32}$`)

type Store struct {
	root string
	db   *sql.DB
	mu   sync.Mutex
}
type Metadata struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}
type Object struct {
	ID       string
	Domain   string
	Metadata []byte
}

func Create(root string, wrappedIdentity []byte) (*Store, error) {
	if err := os.Mkdir(root, 0700); err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			os.RemoveAll(root)
		}
	}()
	for _, domain := range []string{"ordinary", "private"} {
		if err := os.MkdirAll(filepath.Join(root, "objects", domain), 0700); err != nil {
			return nil, err
		}
	}
	for name, data := range map[string][]byte{"format": []byte(Format), "identity.age": wrappedIdentity} {
		if err := atomicfile.WriteNew(filepath.Join(root, name), func(w io.Writer) error { _, e := w.Write(data); return e }); err != nil {
			return nil, err
		}
	}
	f, err := os.OpenFile(filepath.Join(root, "index.db"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	if err = f.Close(); err != nil {
		return nil, err
	}
	db, err := openDB(root)
	if err != nil {
		return nil, err
	}
	_, err = db.Exec(`CREATE TABLE objects (id TEXT PRIMARY KEY, domain TEXT NOT NULL CHECK(domain IN ('ordinary','private')), metadata BLOB NOT NULL); PRAGMA user_version=1;`)
	if err != nil {
		db.Close()
		return nil, err
	}
	ok = true
	return &Store{root: root, db: db}, nil
}

func openDB(root string) (*sql.DB, error) {
	u := url.URL{Scheme: "file", Path: filepath.Join(root, "index.db")}
	db, err := sql.Open("sqlite3", u.String()+"?mode=rw&_journal_mode=WAL&_synchronous=FULL&_foreign_keys=on&_busy_timeout=5000")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err = db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func Open(root string) (*Store, error) {
	marker, err := os.ReadFile(filepath.Join(root, "format"))
	if err != nil || string(marker) != Format {
		return nil, errors.New("missing data directory or unsupported format")
	}
	for _, name := range []string{"index.db", "identity.age"} {
		info, e := os.Lstat(filepath.Join(root, name))
		if e != nil || !info.Mode().IsRegular() {
			return nil, errors.New("missing or invalid store file")
		}
	}
	db, err := openDB(root)
	if err != nil {
		return nil, err
	}
	var version int
	if err = db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 1 {
		db.Close()
		return nil, errors.New("unsupported database version")
	}
	return &Store{root: root, db: db}, nil
}
func (s *Store) Close() error { return s.db.Close() }
func (s *Store) WrappedIdentity() ([]byte, error) {
	return os.ReadFile(filepath.Join(s.root, "identity.age"))
}

func (s *Store) Put(ctx context.Context, name string, src io.Reader, key *age.X25519Identity, private bool) (string, error) {
	if private && key == nil {
		return "", vault.ErrLocked
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	idBytes := make([]byte, 16)
	if _, err := rand.Read(idBytes); err != nil {
		return "", err
	}
	id := hex.EncodeToString(idBytes)
	domain := "ordinary"
	if private {
		domain = "private"
	}
	hash := sha256.New()
	count := &counter{}
	path := filepath.Join(s.root, "objects", domain, id)
	err := atomicfile.WriteNew(path, func(w io.Writer) error {
		r := io.TeeReader(src, io.MultiWriter(hash, count))
		if private {
			return vault.Encrypt(w, r, key.Recipient())
		}
		_, e := io.Copy(w, r)
		return e
	})
	if err != nil {
		return "", err
	}
	meta, err := json.Marshal(Metadata{ID: id, Name: name, SHA256: hex.EncodeToString(hash.Sum(nil)), Size: count.n})
	if err != nil {
		return "", err
	}
	if private {
		var out bytes.Buffer
		if err = vault.Encrypt(&out, bytes.NewReader(meta), key.Recipient()); err != nil {
			return "", err
		}
		clear(meta)
		meta = out.Bytes()
	}
	_, err = s.db.ExecContext(ctx, "INSERT INTO objects(id,domain,metadata) VALUES(?,?,?)", id, domain, meta)
	// An interrupted INSERT may leave an unreferenced immutable object. It must
	// never leave a row pointing to an unpublished file; M1 adds orphan cleanup.
	if err != nil {
		return "", err
	}
	return id, nil
}

type counter struct{ n int64 }

func (c *counter) Write(p []byte) (int, error) { c.n += int64(len(p)); return len(p), nil }

func (s *Store) List(ctx context.Context) ([]Object, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id,domain,metadata FROM objects ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Object
	for rows.Next() {
		var obj Object
		if err = rows.Scan(&obj.ID, &obj.Domain, &obj.Metadata); err != nil {
			return nil, err
		}
		if !validID.MatchString(obj.ID) || (obj.Domain != "ordinary" && obj.Domain != "private") {
			return nil, errors.New("invalid object reference")
		}
		result = append(result, obj)
	}
	return result, rows.Err()
}

// ReadFixture intentionally limits reads; production large-file streaming and
// session authorization belong to M1/M4, not this small verification API.
func (s *Store) ReadFixture(ctx context.Context, id string, key *age.X25519Identity) (Metadata, []byte, error) {
	var meta Metadata
	if !validID.MatchString(id) {
		return meta, nil, errors.New("invalid object id")
	}
	var domain string
	var encoded []byte
	if err := s.db.QueryRowContext(ctx, "SELECT domain,metadata FROM objects WHERE id=?", id).Scan(&domain, &encoded); err != nil {
		return meta, nil, err
	}
	if domain != "ordinary" && domain != "private" {
		return meta, nil, errors.New("invalid domain")
	}
	if domain == "private" {
		if key == nil {
			return meta, nil, vault.ErrLocked
		}
		var err error
		encoded, err = vault.Decrypt(encoded, key, 1<<20)
		if err != nil {
			return meta, nil, err
		}
		defer clear(encoded)
	}
	if err := json.Unmarshal(encoded, &meta); err != nil {
		return meta, nil, err
	}
	if meta.ID != id || meta.Size < 0 || meta.Size > maxFixtureBytes {
		return Metadata{}, nil, errors.New("metadata binding or size invalid")
	}
	f, err := os.Open(filepath.Join(s.root, "objects", domain, id))
	if err != nil {
		return meta, nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxFixtureBytes+1<<20))
	if err != nil {
		return meta, nil, err
	}
	if domain == "private" {
		data, err = vault.Decrypt(data, key, maxFixtureBytes)
		if err != nil {
			return meta, nil, err
		}
	}
	hash := sha256.Sum256(data)
	if int64(len(data)) != meta.Size || hex.EncodeToString(hash[:]) != meta.SHA256 {
		clear(data)
		return Metadata{}, nil, errors.New("object integrity mismatch")
	}
	return meta, data, nil
}

// Snapshot holds the M0 writer lock while copying the index and immutable
// objects. This conservative proof has no GC; M1 will shorten the lock using pins.
func (s *Store) Snapshot(ctx context.Context, dest string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.Mkdir(dest, 0700); err != nil {
		return err
	}
	ok := false
	defer func() {
		if !ok {
			os.RemoveAll(dest)
		}
	}()
	for _, domain := range []string{"ordinary", "private"} {
		if err := os.MkdirAll(filepath.Join(dest, "objects", domain), 0700); err != nil {
			return err
		}
	}
	if err := s.backupDB(ctx, filepath.Join(dest, "index.db")); err != nil {
		return err
	}
	objects, err := s.List(ctx)
	if err != nil {
		return err
	}
	for _, obj := range objects {
		if err = ctx.Err(); err != nil {
			return err
		}
		rel := filepath.Join("objects", obj.Domain, obj.ID)
		if err = atomicfile.CopyNew(filepath.Join(dest, rel), filepath.Join(s.root, rel)); err != nil {
			return err
		}
	}
	for _, name := range []string{"format", "identity.age"} {
		if err = atomicfile.CopyNew(filepath.Join(dest, name), filepath.Join(s.root, name)); err != nil {
			return err
		}
	}
	ok = true
	return nil
}

func (s *Store) backupDB(ctx context.Context, path string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	u := url.URL{Scheme: "file", Path: path}
	dst, err := sql.Open("sqlite3", u.String()+"?mode=rw&_synchronous=FULL")
	if err != nil {
		return err
	}
	defer dst.Close()
	srcConn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer srcConn.Close()
	dstConn, err := dst.Conn(ctx)
	if err != nil {
		return err
	}
	defer dstConn.Close()
	return srcConn.Raw(func(src any) error {
		return dstConn.Raw(func(dest any) error {
			backup, err := dest.(*sqlite3.SQLiteConn).Backup("main", src.(*sqlite3.SQLiteConn), "main")
			if err != nil {
				return err
			}
			defer backup.Close()
			for {
				if err = ctx.Err(); err != nil {
					return err
				}
				done, e := backup.Step(128)
				if e != nil {
					return fmt.Errorf("SQLite backup: %w", e)
				}
				if done {
					return backup.Finish()
				}
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(5 * time.Millisecond):
				}
			}
		})
	})
}
