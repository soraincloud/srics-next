package library

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"time"

	"filippo.io/age"
	"github.com/soraincloud/srics-next/internal/atomicfile"
	"github.com/soraincloud/srics-next/internal/media"
	"github.com/soraincloud/srics-next/internal/vault"
)

const ChunkSize int64 = 8 << 20

type Transfer struct {
	ID      string   `json:"id"`
	Module  string   `json:"module"`
	Name    string   `json:"name"`
	Size    int64    `json:"size"`
	Parent  string   `json:"parent"`
	Index   int      `json:"index"`
	Hashes  []string `json:"hashes"`
	Done    []int    `json:"done"`
	State   string   `json:"state"`
	Created string   `json:"created"`
}

func migrateTransfers(db *sql.DB, root string, existing bool) error {
	if existing {
		dest := filepath.Join(root, "staging", "before-transfers-"+NewID()+".db")
		f, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		if err = f.Close(); err != nil {
			return err
		}
		if _, err := db.Exec("VACUUM INTO ?", dest); err != nil {
			return err
		}
		if err := os.Chmod(dest, 0600); err != nil {
			return err
		}
	}
	if err := os.Mkdir(filepath.Join(root, "chunks"), 0700); err != nil && !os.IsExist(err) {
		return err
	}
	_, err := db.Exec(`BEGIN; CREATE TABLE transfers(id TEXT PRIMARY KEY,private INTEGER NOT NULL,payload BLOB NOT NULL);
 CREATE TABLE transfer_chunks(transfer_id TEXT NOT NULL REFERENCES transfers(id) ON DELETE CASCADE,idx INTEGER NOT NULL,object TEXT NOT NULL,hash TEXT NOT NULL,PRIMARY KEY(transfer_id,idx)); PRAGMA user_version=4; COMMIT;`)
	return err
}
func privateTransfer(t Transfer) bool { return t.Module == "private" || t.Module == "files" }
func (l *Library) transfer(id string, a *vault.Access) (Transfer, error) {
	var t Transfer
	if err := l.Check(); err != nil {
		return t, err
	}
	var payload []byte
	var private bool
	err := l.db.QueryRow("SELECT private,payload FROM transfers WHERE id=?", id).Scan(&private, &payload)
	if errors.Is(err, sql.ErrNoRows) {
		return t, ErrMissing
	}
	if err != nil {
		return t, err
	}
	if private != (a != nil) {
		return t, ErrMissing
	}
	if private {
		key, _, _, e := a.Snapshot()
		if e != nil {
			return t, e
		}
		payload, e = vault.Decrypt(payload, key, 2<<20)
		if e != nil {
			return t, e
		}
		defer clear(payload)
	}
	if err = json.Unmarshal(payload, &t); err != nil {
		return t, err
	}
	if t.ID != id || privateTransfer(t) != private {
		return t, errors.New("上传任务校验失败")
	}
	rows, err := l.db.Query("SELECT idx FROM transfer_chunks WHERE transfer_id=? ORDER BY idx", id)
	if err != nil {
		return t, err
	}
	defer rows.Close()
	t.Done = []int{}
	for rows.Next() {
		var i int
		if err = rows.Scan(&i); err != nil {
			return t, err
		}
		t.Done = append(t.Done, i)
	}
	return t, rows.Err()
}
func (l *Library) Transfers(a *vault.Access) ([]Transfer, error) {
	if err := l.Check(); err != nil {
		return nil, err
	}
	rows, err := l.db.Query("SELECT id FROM transfers WHERE private=? ORDER BY rowid DESC", a != nil)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			break
		}
		ids = append(ids, id)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	out := []Transfer{}
	for _, id := range ids {
		t, e := l.transfer(id, a)
		if e != nil {
			return nil, e
		}
		if t.State != "complete" {
			out = append(out, t)
		}
	}
	return out, nil
}
func (l *Library) saveTransfer(ctx context.Context, t Transfer, a *vault.Access) error {
	t.Done = nil
	payload, err := json.Marshal(t)
	if err != nil {
		return err
	}
	if a != nil {
		defer clear(payload)
		key, _, _, e := a.Snapshot()
		if e != nil {
			return e
		}
		var b bytes.Buffer
		if e = vault.Encrypt(&b, bytes.NewReader(payload), key.Recipient()); e != nil {
			return e
		}
		payload = b.Bytes()
	}
	commit := func() error {
		if err := l.Check(); err != nil {
			return err
		}
		_, e := l.db.Exec("INSERT INTO transfers(id,private,payload) VALUES(?,?,?) ON CONFLICT(id) DO UPDATE SET payload=excluded.payload", t.ID, a != nil, payload)
		return e
	}
	if a != nil {
		return a.Commit(ctx, commit)
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	return commit()
}
func (l *Library) CreateTransfer(ctx context.Context, t Transfer, a *vault.Access) (Transfer, error) {
	l.transferMu.Lock()
	defer l.transferMu.Unlock()
	l.mu.Lock()
	defer l.mu.Unlock()
	if !IDPattern.MatchString(t.ID) || privateTransfer(t) != (a != nil) || t.Size < 0 {
		return t, errors.New("无效上传任务")
	}
	limit := int64(MaxFile)
	if t.Module == "files" {
		limit = MaxPrivateFile
	}
	if t.Module == "attachments" {
		limit = MaxOrdinaryFile
	}
	if t.Size > limit || len(t.Hashes) != int((t.Size+ChunkSize-1)/ChunkSize) {
		return t, errors.New("上传大小或分块数量不正确")
	}
	for _, h := range t.Hashes {
		if len(h) != 64 {
			return t, errors.New("无效分块校验值")
		}
		if _, err := hex.DecodeString(h); err != nil {
			return t, err
		}
	}
	if a == nil {
		up, err := l.Upload(t.Parent)
		if err != nil {
			return t, err
		}
		if t.Index < 0 || t.Index >= len(up.Files) || up.State == "cancelled" || up.Module != t.Module || up.Files[t.Index].Name != t.Name || up.Files[t.Index].Size != t.Size {
			return t, ErrConflict
		}
	} else {
		if t.Parent != "" {
			return t, ErrConflict
		}
		var err error
		t.Name, err = CleanName(t.Name)
		if err != nil {
			return t, err
		}
	}
	old, err := l.transfer(t.ID, a)
	if err == nil {
		if old.Module != t.Module || old.Parent != t.Parent || old.Index != t.Index || old.Name != t.Name || old.Size != t.Size || !reflect.DeepEqual(old.Hashes, t.Hashes) {
			return t, errors.New("文件内容与原上传任务不同，请选择原文件或新建任务")
		}
		return old, nil
	}
	if !errors.Is(err, ErrMissing) {
		return t, err
	}
	// IDs are global even across the ordinary and private namespaces.
	var count int
	if err = l.db.QueryRow("SELECT count(*) FROM transfers WHERE id=?", t.ID).Scan(&count); err != nil {
		return t, err
	}
	if count != 0 {
		return t, ErrConflict
	}
	t.State = "pending"
	t.Created = time.Now().UTC().Format(time.RFC3339Nano)
	t.Done = []int{}
	return t, l.saveTransfer(ctx, t, a)
}
func (l *Library) ReceiveChunk(ctx context.Context, id string, index int, src io.Reader, a *vault.Access) (Transfer, error) {
	l.transferMu.Lock()
	defer l.transferMu.Unlock()
	l.objectsMu.RLock()
	defer l.objectsMu.RUnlock()
	l.mu.Lock()
	defer l.mu.Unlock()
	t, err := l.transfer(id, a)
	if err != nil {
		return t, err
	}
	if index < 0 || index >= len(t.Hashes) {
		return t, errors.New("无效分块序号")
	}
	expected := min(ChunkSize, t.Size-int64(index)*ChunkSize)
	if err = l.NeedSpace(expected + 1<<20); err != nil {
		return t, err
	}
	data, err := io.ReadAll(io.LimitReader(vault.ContextReader{Ctx: ctx, Reader: src}, ChunkSize+1))
	if err != nil {
		return t, err
	}
	defer clear(data)
	h := sha256.Sum256(data)
	if int64(len(data)) != expected || hex.EncodeToString(h[:]) != t.Hashes[index] {
		return t, errors.New("分块校验失败，请重试原文件")
	}
	for _, done := range t.Done {
		if done == index {
			return t, nil
		}
	}
	if t.State == "complete" {
		return t, nil
	}
	object := NewID()
	dest := filepath.Join(l.Root, "chunks", object)
	cipherHash := sha256.New()
	err = atomicfile.WriteNew(dest, func(w io.Writer) error {
		w = io.MultiWriter(w, cipherHash)
		if a == nil {
			_, e := w.Write(data)
			return e
		}
		key, _, _, e := a.Snapshot()
		if e != nil {
			return e
		}
		return vault.Encrypt(w, bytes.NewReader(data), key.Recipient())
	})
	if err != nil {
		return t, err
	}
	commit := func() error {
		if e := ctx.Err(); e != nil {
			return e
		}
		if e := l.Check(); e != nil {
			return e
		}
		_, e := l.db.Exec("INSERT INTO transfer_chunks(transfer_id,idx,object,hash) VALUES(?,?,?,?)", id, index, object, hex.EncodeToString(cipherHash.Sum(nil)))
		return e
	}
	if a != nil {
		err = a.Commit(ctx, commit)
	} else {
		err = commit()
	}
	if err != nil {
		os.Remove(dest)
		return t, err
	}
	t.Done = append(t.Done, index)
	return t, nil
}

type chunkRef struct {
	object, hash string
	index        int
}
type chunkReader struct {
	ctx  context.Context
	root string
	t    Transfer
	refs []chunkRef
	key  age.Identity
	next int
	data []byte
}

func (r *chunkReader) Close() error { clear(r.data); r.data = nil; return nil }
func (r *chunkReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	if len(r.data) == 0 {
		if r.next == len(r.refs) {
			return 0, io.EOF
		}
		ref := r.refs[r.next]
		r.next++
		if ref.index != r.next-1 || !IDPattern.MatchString(ref.object) {
			return 0, errors.New("分块引用不完整")
		}
		f, e := os.Open(filepath.Join(r.root, "chunks", ref.object))
		if e != nil {
			return 0, e
		}
		b, e := io.ReadAll(io.LimitReader(f, ChunkSize+1<<20))
		f.Close()
		if e != nil {
			return 0, e
		}
		h := sha256.Sum256(b)
		if hex.EncodeToString(h[:]) != ref.hash {
			clear(b)
			return 0, errors.New("已保存分块损坏，请取消任务并重新上传")
		}
		if r.key != nil {
			plain, e := vault.Decrypt(b, r.key, ChunkSize)
			clear(b)
			if e != nil {
				return 0, e
			}
			b = plain
		}
		h = sha256.Sum256(b)
		if hex.EncodeToString(h[:]) != r.t.Hashes[ref.index] {
			clear(b)
			return 0, errors.New("分块内容不一致")
		}
		r.data = b
	}
	n := copy(p, r.data)
	clear(r.data[:n])
	r.data = r.data[n:]
	return n, nil
}
func (l *Library) FinishTransfer(ctx context.Context, id string, a *vault.Access, c media.Converter) (any, error) {
	l.transferMu.Lock()
	defer l.transferMu.Unlock()
	t, err := l.transfer(id, a)
	if err != nil {
		return nil, err
	}
	if t.State == "complete" {
		if a != nil {
			return l.PrivateItem(a, id)
		}
		return l.Upload(t.Parent)
	}
	if len(t.Done) != len(t.Hashes) {
		return nil, errors.New("分块尚未传完")
	}
	rows, err := l.db.Query("SELECT idx,object,hash FROM transfer_chunks WHERE transfer_id=? ORDER BY idx", id)
	if err != nil {
		return nil, err
	}
	refs := []chunkRef{}
	for rows.Next() {
		var ref chunkRef
		if err = rows.Scan(&ref.index, &ref.object, &ref.hash); err != nil {
			break
		}
		refs = append(refs, ref)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	r := &chunkReader{ctx: ctx, root: l.Root, t: t, refs: refs}
	defer r.Close()
	if a != nil {
		key, _, _, e := a.Snapshot()
		if e != nil {
			return nil, e
		}
		r.key = key
	}
	var result any
	if a != nil {
		result, err = l.ReceivePrivate(ctx, a, id, t.Module, t.Name, t.Size, r)
	} else {
		h := sha256.New()
		if _, err = io.Copy(h, r); err == nil {
			r.next = 0
			result, err = l.Receive(ctx, t.Parent, t.Index, fmt.Sprintf("%x", h.Sum(nil)), r, c)
		}
	}
	if err != nil {
		return nil, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	t.State = "complete"
	if err = l.saveTransfer(ctx, t, a); err != nil {
		return nil, err
	}
	// Commit success before removing resumable chunks. A retry returns the existing object.
	if _, err = l.db.Exec("DELETE FROM transfer_chunks WHERE transfer_id=?", id); err != nil {
		return nil, err
	}
	for _, ref := range refs {
		os.Remove(filepath.Join(l.Root, "chunks", ref.object))
	}
	return result, nil
}
func (l *Library) CancelTransfer(ctx context.Context, id string, a *vault.Access) error {
	l.transferMu.Lock()
	defer l.transferMu.Unlock()
	l.mu.Lock()
	defer l.mu.Unlock()
	t, err := l.transfer(id, a)
	if err != nil {
		return err
	}
	if t.State == "complete" {
		return errors.New("已完成资料请从资料库删除")
	}
	commit := func() error { _, err := l.db.Exec("DELETE FROM transfers WHERE id=?", id); return err }
	if a != nil {
		return a.Commit(ctx, commit)
	}
	return commit()
}

func (l *Library) verifyChunks(ctx context.Context) error {
	rows, err := l.db.Query("SELECT object,hash FROM transfer_chunks")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, want string
		if err = rows.Scan(&id, &want); err != nil {
			return err
		}
		if !IDPattern.MatchString(id) {
			return errors.New("无效分块引用")
		}
		f, e := os.Open(filepath.Join(l.Root, "chunks", id))
		if e != nil {
			return e
		}
		h := sha256.New()
		_, e = io.Copy(h, vault.ContextReader{Ctx: ctx, Reader: f})
		f.Close()
		if e != nil {
			return e
		}
		if fmt.Sprintf("%x", h.Sum(nil)) != want {
			return errors.New("上传分块校验失败")
		}
	}
	return rows.Err()
}
