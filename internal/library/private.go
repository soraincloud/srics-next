package library

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"time"

	"filippo.io/age"
	"github.com/soraincloud/srics-next/internal/atomicfile"
	"github.com/soraincloud/srics-next/internal/vault"
	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

const MaxPrivateFile int64 = 10 << 30
const MaxPrivatePhoto int64 = 64 << 20

type PrivateItem struct {
	ID         string `json:"id"`
	Module     string `json:"module"`
	Name       string `json:"name"`
	Original   string `json:"original"`
	Size       int64  `json:"size"`
	SHA256     string `json:"sha256"`
	Created    string `json:"created"`
	Deleted    string `json:"deleted"`
	Revision   int    `json:"revision"`
	Object     string `json:"-"`
	Thumb      string `json:"-"`
	Seq        int64  `json:"seq"`
	CipherHash string `json:"-"`
	ThumbHash  string `json:"-"`
}

// Bind logical metadata to the immutable ciphertext references. Only this JSON
// is decrypted; names, timestamps, plaintext hashes never appear in SQL values.
type privateEnvelope struct {
	Item      PrivateItem `json:"item"`
	Object    string      `json:"object"`
	Thumb     string      `json:"thumb"`
	Hash      string      `json:"hash"`
	ThumbHash string      `json:"thumbHash"`
}
type privateRow struct {
	id, object, thumb, hash, thumbHash string
	seq                                int64
	payload                            []byte
}

func migratePrivate(db *sql.DB, root string, existing bool) error {
	if existing {
		path := filepath.Join(root, "staging", "before-private-"+NewID()+".db")
		f, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			return e
		}
		f.Close()
		if _, e = db.Exec("VACUUM INTO ?", path); e != nil {
			return e
		}
	}
	dir := filepath.Join(root, "private-objects")
	if err := os.Mkdir(dir, 0700); err != nil && !os.IsExist(err) {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("私密资料目录无效")
	}
	_, err = db.Exec(`BEGIN; CREATE TABLE private_items(seq INTEGER PRIMARY KEY AUTOINCREMENT,id TEXT NOT NULL UNIQUE,payload BLOB NOT NULL,object TEXT NOT NULL,thumb TEXT NOT NULL,hash TEXT NOT NULL,thumb_hash TEXT NOT NULL); PRAGMA user_version=3; COMMIT;`)
	return err
}
func (l *Library) PrepareVault(passphrase, current string) ([]byte, error) {
	if len(passphrase) < 12 || len(passphrase) > 1024 {
		return nil, errors.New("保险库口令需为 12–1024 字节")
	}
	wrapped, err := l.Setting("vault-key")
	if err != nil {
		return nil, err
	}
	if len(wrapped) == 0 {
		_, wrapped, err = vault.Create(passphrase)
		return wrapped, err
	}
	key, err := vault.Unlock(wrapped, current)
	if err != nil {
		return nil, errors.New("当前保险库口令不正确")
	}
	return vault.Wrap(key, passphrase)
}
func (l *Library) PrivatePath(id string) string {
	if !IDPattern.MatchString(id) {
		return ""
	}
	return filepath.Join(l.Root, "private-objects", id)
}
func (l *Library) privateRows() ([]privateRow, error) {
	if err := l.Check(); err != nil {
		return nil, err
	}
	rows, err := l.db.Query("SELECT id,seq,payload,object,thumb,hash,thumb_hash FROM private_items ORDER BY seq DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []privateRow{}
	for rows.Next() {
		var r privateRow
		if err = rows.Scan(&r.id, &r.seq, &r.payload, &r.object, &r.thumb, &r.hash, &r.thumbHash); err != nil {
			return nil, err
		}
		if !IDPattern.MatchString(r.id) || !IDPattern.MatchString(r.object) || !validHash(r.hash) || len(r.payload) == 0 || (r.thumb == "" && r.thumbHash != "") || (r.thumb != "" && (!IDPattern.MatchString(r.thumb) || !validHash(r.thumbHash))) {
			return nil, errors.New("私密文件索引损坏，已停止清理，请从备份恢复")
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func (l *Library) privateRow(id string) (privateRow, error) {
	var r privateRow
	err := l.db.QueryRow("SELECT id,seq,payload,object,thumb,hash,thumb_hash FROM private_items WHERE id=?", id).Scan(&r.id, &r.seq, &r.payload, &r.object, &r.thumb, &r.hash, &r.thumbHash)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrMissing
	}
	return r, err
}
func decryptPrivate(r privateRow, key age.Identity) (PrivateItem, error) {
	plain, err := vault.Decrypt(r.payload, key, 16<<10)
	if err != nil {
		return PrivateItem{}, errors.New("私密索引无法解密")
	}
	defer clear(plain)
	var e privateEnvelope
	if json.Unmarshal(plain, &e) != nil || e.Item.ID != r.id || e.Object != r.object || e.Thumb != r.thumb || e.Hash != r.hash || e.ThumbHash != r.thumbHash {
		return PrivateItem{}, errors.New("私密索引校验失败")
	}
	e.Item.Object = r.object
	e.Item.Thumb = r.thumb
	e.Item.Seq = r.seq
	e.Item.CipherHash = r.hash
	e.Item.ThumbHash = r.thumbHash
	return e.Item, nil
}
func encodePrivate(it PrivateItem, r privateRow, key *age.X25519Identity) ([]byte, error) {
	plain, err := json.Marshal(privateEnvelope{it, r.object, r.thumb, r.hash, r.thumbHash})
	if err != nil {
		return nil, err
	}
	defer clear(plain)
	var b bytes.Buffer
	err = vault.Encrypt(&b, bytes.NewReader(plain), key.Recipient())
	return b.Bytes(), err
}
func (l *Library) PrivateItems(ctx context.Context, a *vault.Access) ([]PrivateItem, error) {
	key, _, _, err := a.Snapshot()
	if err != nil {
		return nil, err
	}
	rows, err := l.privateRows()
	if err != nil {
		return nil, err
	}
	out := []PrivateItem{}
	for _, r := range rows {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		it, e := decryptPrivate(r, key)
		if e != nil {
			return nil, e
		}
		out = append(out, it)
	}
	return out, nil
}
func (l *Library) PrivateItem(a *vault.Access, id string) (PrivateItem, error) {
	key, _, _, err := a.Snapshot()
	if err != nil {
		return PrivateItem{}, err
	}
	if err = l.Check(); err != nil {
		return PrivateItem{}, err
	}
	r, err := l.privateRow(id)
	if err != nil {
		return PrivateItem{}, err
	}
	return decryptPrivate(r, key)
}
func (l *Library) privatePut(ctx context.Context, src io.Reader, key *age.X25519Identity) (string, string, error) {
	id := NewID()
	h := sha256.New()
	err := atomicfile.WriteNew(l.PrivatePath(id), func(w io.Writer) error {
		return vault.Encrypt(io.MultiWriter(w, h), vault.ContextReader{Ctx: ctx, Reader: src}, key.Recipient())
	})
	if err != nil {
		return "", "", err
	}
	return id, hex.EncodeToString(h.Sum(nil)), nil
}
func privateThumbnail(data []byte) ([]byte, error) {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, errors.New("照片格式暂不支持，请使用 JPEG、PNG、WebP 或 GIF")
	}
	if cfg.Width < 1 || cfg.Height < 1 || int64(cfg.Width)*int64(cfg.Height) > 40_000_000 {
		return nil, errors.New("照片最多支持 4000 万像素")
	}
	if format != "jpeg" && format != "png" && format != "webp" && format != "gif" {
		return nil, errors.New("照片格式暂不支持")
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, errors.New("照片无法解码")
	}
	w, h := cfg.Width, cfg.Height
	if w > 640 || h > 640 {
		if w >= h {
			h = max(1, h*640/w)
			w = 640
		} else {
			w = max(1, w*640/h)
			h = 640
		}
	}
	small := image.NewNRGBA(image.Rect(0, 0, w, h))
	defer clear(small.Pix)
	draw.ApproxBiLinear.Scale(small, small.Bounds(), img, img.Bounds(), draw.Src, nil)
	var out bytes.Buffer
	err = png.Encode(&out, small)
	return out.Bytes(), err
}
func (l *Library) ReceivePrivate(ctx context.Context, a *vault.Access, id, module, name string, size int64, src io.Reader) (PrivateItem, error) {
	l.objectsMu.RLock()
	defer l.objectsMu.RUnlock()
	var zero PrivateItem
	if !IDPattern.MatchString(id) || (module != "private" && module != "files") {
		return zero, errors.New("无效私密上传")
	}
	name, err := CleanName(name)
	if err != nil {
		return zero, err
	}
	limit := MaxPrivateFile
	if module == "private" {
		limit = MaxPrivatePhoto
	}
	if size < 0 || size > limit {
		return zero, errors.New("文件超出上传大小限制")
	}
	if err = l.NeedSpace(size + size/100 + 2<<20); err != nil {
		return zero, err
	}
	key, _, _, err := a.Snapshot()
	if err != nil {
		return zero, err
	}
	h := sha256.New()
	count := &countWriter{}
	reader := io.TeeReader(io.LimitReader(src, size+1), io.MultiWriter(h, count))
	var photo bytes.Buffer
	if module == "private" {
		reader = io.TeeReader(reader, &photo)
		defer func() { clear(photo.Bytes()) }()
	}
	r := privateRow{id: id}
	r.object, r.hash, err = l.privatePut(ctx, reader, key)
	if err != nil {
		return zero, err
	}
	published := false
	defer func() {
		if !published {
			os.Remove(l.PrivatePath(r.object))
			if r.thumb != "" {
				os.Remove(l.PrivatePath(r.thumb))
			}
		}
	}()
	if count.n != size {
		return zero, errors.New("文件传输未完成或大小不一致")
	}
	if module == "private" {
		thumb, e := privateThumbnail(photo.Bytes())
		if e != nil {
			return zero, e
		}
		defer clear(thumb)
		r.thumb, r.thumbHash, err = l.privatePut(ctx, bytes.NewReader(thumb), key)
		if err != nil {
			return zero, err
		}
	}
	it := PrivateItem{ID: id, Module: module, Name: name, Original: name, Size: size, SHA256: hex.EncodeToString(h.Sum(nil)), Created: time.Now().UTC().Format(time.RFC3339Nano), Revision: 1, Object: r.object, Thumb: r.thumb}
	l.mu.Lock()
	defer l.mu.Unlock()
	err = a.Commit(ctx, func() error {
		if err := l.Check(); err != nil {
			return err
		}
		old, e := l.privateRow(id)
		if e == nil {
			prior, e := decryptPrivate(old, key)
			if e != nil {
				return e
			}
			if prior.Module != module || prior.Original != name || prior.Size != size || prior.SHA256 != it.SHA256 || prior.Deleted != "" {
				return ErrConflict
			}
			it = prior
			return nil
		}
		if !errors.Is(e, ErrMissing) {
			return e
		}
		r.payload, e = encodePrivate(it, r, key)
		if e != nil {
			return e
		}
		res, e := l.db.Exec("INSERT INTO private_items(id,payload,object,thumb,hash,thumb_hash) VALUES(?,?,?,?,?,?)", id, r.payload, r.object, r.thumb, r.hash, r.thumbHash)
		if e == nil {
			it.Seq, _ = res.LastInsertId()
			published = true
		}
		return e
	})
	return it, err
}

type countWriter struct{ n int64 }

func (w *countWriter) Write(b []byte) (int, error) { w.n += int64(len(b)); return len(b), nil }
func (l *Library) ChangePrivate(ctx context.Context, a *vault.Access, id, name, action string, revision int) (PrivateItem, error) {
	key, _, _, err := a.Snapshot()
	if err != nil {
		return PrivateItem{}, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	r, err := l.privateRow(id)
	if err != nil {
		return PrivateItem{}, err
	}
	it, err := decryptPrivate(r, key)
	if err != nil {
		return it, err
	}
	if it.Revision != revision {
		return it, ErrConflict
	}
	switch action {
	case "rename":
		if it.Module != "files" || it.Deleted != "" {
			return it, ErrConflict
		}
		it.Name, err = CleanName(name)
	case "trash":
		it.Deleted = time.Now().UTC().Format(time.RFC3339Nano)
	case "restore":
		it.Deleted = ""
	default:
		return it, errors.New("无效操作")
	}
	if err != nil {
		return it, err
	}
	it.Revision++
	payload, err := encodePrivate(it, r, key)
	if err != nil {
		return it, err
	}
	err = a.Commit(ctx, func() error {
		if e := l.Check(); e != nil {
			return e
		}
		_, e := l.db.Exec("UPDATE private_items SET payload=? WHERE id=?", payload, id)
		return e
	})
	return it, err
}

// PrivateRead verifies the entire authenticated stream before returning a second
// pass on the SAME open immutable file. No plaintext temporary file is needed.
// The caller must abort its HTTP stream if the second pass fails (never close a
// ZIP successfully after an authentication or cancellation error).
func (l *Library) PrivateRead(ctx context.Context, a *vault.Access, it PrivateItem, thumb bool) (io.ReadCloser, int64, error) {
	key, _, _, err := a.Snapshot()
	if err != nil {
		return nil, 0, err
	}
	path := it.Object
	if thumb {
		path = it.Thumb
	}
	if path == "" {
		return nil, 0, ErrMissing
	}
	f, err := os.Open(l.PrivatePath(path))
	if err != nil {
		return nil, 0, err
	}
	fail := func(e error) (io.ReadCloser, int64, error) { f.Close(); return nil, 0, e }
	cipherHash := sha256.New()
	plain, err := age.Decrypt(vault.ContextReader{Ctx: ctx, Reader: io.TeeReader(f, cipherHash)}, key)
	if err != nil {
		return fail(errors.New("私密文件校验失败"))
	}
	h := sha256.New()
	n, err := io.Copy(h, vault.ContextReader{Ctx: ctx, Reader: plain})
	if err != nil {
		return fail(errors.New("私密文件校验失败或已锁定"))
	}
	expectedHash := it.CipherHash
	if thumb {
		expectedHash = it.ThumbHash
	}
	if expectedHash != "" && hex.EncodeToString(cipherHash.Sum(nil)) != expectedHash {
		return fail(errors.New("私密密文引用校验失败"))
	}
	if !thumb && (n != it.Size || hex.EncodeToString(h.Sum(nil)) != it.SHA256) {
		return fail(errors.New("私密文件内容不一致"))
	}
	if thumb && n > 8<<20 {
		return fail(errors.New("私密预览过大"))
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return fail(err)
	}
	plain, err = age.Decrypt(vault.ContextReader{Ctx: ctx, Reader: f}, key)
	if err != nil {
		return fail(err)
	}
	return &privateReader{Reader: vault.ContextReader{Ctx: ctx, Reader: plain}, file: f}, n, nil
}

type privateReader struct {
	io.Reader
	file *os.File
}

func (r *privateReader) Close() error { return r.file.Close() }
func (l *Library) verifyPrivate(ctx context.Context) error {
	rows, err := l.privateRows()
	if err != nil {
		return err
	}
	for _, r := range rows {
		for _, v := range [][2]string{{r.object, r.hash}, {r.thumb, r.thumbHash}} {
			if v[0] == "" {
				continue
			}
			f, e := os.Open(l.PrivatePath(v[0]))
			if e != nil {
				return e
			}
			h := sha256.New()
			_, e = io.Copy(h, vault.ContextReader{Ctx: ctx, Reader: f})
			f.Close()
			if e != nil {
				return e
			}
			if hex.EncodeToString(h.Sum(nil)) != v[1] {
				return errors.New("私密文件密文校验失败")
			}
		}
	}
	return nil
}
