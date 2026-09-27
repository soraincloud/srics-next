// Package library stores the persistent user library. The M0 store remains a
// separate synthetic verification fixture and never opens this directory.
package library

import (
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
	"strings"
	"sync"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/soraincloud/srics-next/internal/atomicfile"
	"golang.org/x/sys/unix"
)

const Format = "srics-library-v1\n"
const MaxFile = 64 << 20

var IDPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)
var ErrConflict = errors.New("资料已变化，请刷新后重试")
var ErrMissing = errors.New("找不到这项资料")

type Library struct {
	Root       string
	db         *sql.DB
	mu         sync.Mutex
	transferMu sync.Mutex
	objectsMu  sync.RWMutex
	marker     []byte
	inode      os.FileInfo
	lock       *os.File
	FreeSpace  func(string) (uint64, error)
}
type Item struct {
	ID       string   `json:"id"`
	Module   string   `json:"module"`
	Name     string   `json:"name"`
	Tags     []string `json:"tags"`
	Created  string   `json:"created"`
	Deleted  string   `json:"deleted"`
	Revision int      `json:"revision"`
	Seq      int64    `json:"seq"`
	Pages    []Page   `json:"pages"`
	Progress int      `json:"progress"`
}
type Page struct {
	Name   string `json:"name"`
	Object string `json:"object"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
	MIME   string `json:"mime"`
	Thumb  string `json:"thumb"`
}
type UploadFile struct {
	Name       string `json:"name"`
	Size       int64  `json:"size"`
	SourceHash string `json:"sourceHash,omitempty"`
	Page       *Page  `json:"page,omitempty"`
}
type Upload struct {
	ID      string       `json:"id"`
	Module  string       `json:"module"`
	Name    string       `json:"name"`
	Tags    []string     `json:"tags"`
	Files   []UploadFile `json:"files"`
	State   string       `json:"state"`
	Error   string       `json:"error"`
	Created string       `json:"created"`
}

func NewID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
func ValidModule(m string) bool {
	return m == "comics" || m == "images" || m == "photos" || m == "novels"
}
func Create(root string) error {
	if err := os.Mkdir(root, 0700); err != nil {
		return err
	}
	ok := false
	defer func() {
		if !ok {
			os.RemoveAll(root)
		}
	}()
	for _, name := range []string{"objects", "staging"} {
		if err := os.Mkdir(filepath.Join(root, name), 0700); err != nil {
			return err
		}
	}
	if err := atomicfile.WriteNew(filepath.Join(root, "format"), func(w io.Writer) error { _, e := io.WriteString(w, Format+NewID()+"\n"); return e }); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(root, "index.db"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	l, err := Open(root)
	if err != nil {
		return err
	}
	err = l.Close()
	ok = err == nil
	return err
}
func Open(root string) (*Library, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	marker, err := os.ReadFile(filepath.Join(root, "format"))
	if err != nil || len(marker) != len(Format)+33 || !strings.HasPrefix(string(marker), Format) {
		return nil, errors.New("资料目录缺失或格式不兼容，已停止；不会自动创建替代目录")
	}
	for _, name := range []string{"index.db", "objects", "staging"} {
		info, e := os.Lstat(filepath.Join(root, name))
		if e != nil || info.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("资料目录不完整或包含无效链接")
		}
	}
	lock, err := os.OpenFile(filepath.Join(root, ".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		lock.Close()
		return nil, errors.New("资料库已由另一个服务使用，请先停止那个服务")
	}
	keepLock := false
	defer func() {
		if !keepLock {
			_ = unix.Flock(int(lock.Fd()), unix.LOCK_UN)
			_ = lock.Close()
		}
	}()
	u := url.URL{Scheme: "file", Path: filepath.Join(root, "index.db")}
	db, err := sql.Open("sqlite3", u.String()+"?mode=rw&_journal_mode=WAL&_synchronous=FULL&_foreign_keys=on&_busy_timeout=5000")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	fail := func(e error) (*Library, error) { db.Close(); return nil, e }
	var version int
	if err = db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return fail(err)
	}
	if version > 4 {
		return fail(errors.New("数据版本较新，请升级程序后打开"))
	}
	if version == 0 {
		tx, e := db.Begin()
		if e != nil {
			return fail(e)
		}
		_, e = tx.Exec(`CREATE TABLE settings(key TEXT PRIMARY KEY,value BLOB NOT NULL);
CREATE TABLE uploads(id TEXT PRIMARY KEY,data TEXT NOT NULL);
CREATE TABLE items(seq INTEGER PRIMARY KEY AUTOINCREMENT,id TEXT NOT NULL UNIQUE,module TEXT NOT NULL,name TEXT NOT NULL,tags TEXT NOT NULL,created TEXT NOT NULL,deleted TEXT NOT NULL DEFAULT '',revision INTEGER NOT NULL DEFAULT 1,pages TEXT NOT NULL,progress INTEGER NOT NULL DEFAULT 0);
CREATE INDEX items_module ON items(module,deleted,seq);
PRAGMA user_version=1;`)
		if e != nil {
			tx.Rollback()
			return fail(e)
		}
		if e = tx.Commit(); e != nil {
			return fail(e)
		}
	}
	if version < 2 {
		if err = migrateNovels(db, root, version == 1); err != nil {
			return fail(err)
		}
	}
	if version < 3 {
		if err = migratePrivate(db, root, version > 0); err != nil {
			return fail(err)
		}
	}
	if version < 4 {
		if err = migrateTransfers(db, root, version > 0); err != nil {
			return fail(err)
		}
	}
	if info, err := os.Lstat(filepath.Join(root, "chunks")); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fail(errors.New("上传分块目录缺失或无效"))
	}
	info, e := os.Lstat(filepath.Join(root, "private-objects"))
	if e != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fail(errors.New("私密资料目录缺失或无效"))
	}
	inode, err := os.Stat(filepath.Join(root, "index.db"))
	if err != nil {
		return fail(err)
	}
	l := &Library{lock: lock, Root: root, db: db, marker: marker, inode: inode, FreeSpace: freeSpace}
	if err = l.Check(); err != nil {
		l.Close()
		return nil, err
	}
	// Only abandoned atomic writes are removed. Published immutable objects are
	// never collected here: completed uploads and backup snapshots may refer to them.
	for _, dir := range []string{"objects", "staging", "private-objects", "chunks"} {
		matches, _ := filepath.Glob(filepath.Join(root, dir, ".pending-*"))
		for _, p := range matches {
			os.Remove(p)
		}
	}
	keepLock = true
	return l, nil
}
func (l *Library) Close() error {
	err := l.db.Close()
	if l.lock != nil {
		_ = unix.Flock(int(l.lock.Fd()), unix.LOCK_UN)
		_ = l.lock.Close()
	}
	return err
}
func (l *Library) Check() error {
	marker, err := os.ReadFile(filepath.Join(l.Root, "format"))
	if err != nil || string(marker) != string(l.marker) {
		return errors.New("资料盘已断开或被替换，请恢复原目录后重试")
	}
	info, err := os.Stat(filepath.Join(l.Root, "index.db"))
	if err != nil || !os.SameFile(l.inode, info) {
		return errors.New("资料索引已被替换，请停止服务并检查资料盘")
	}
	return nil
}
func freeSpace(path string) (uint64, error) {
	var s unix.Statfs_t
	if err := unix.Statfs(path, &s); err != nil {
		return 0, err
	}
	return uint64(s.Bavail) * uint64(s.Bsize), nil
}
func (l *Library) NeedSpace(n int64) error {
	if err := l.Check(); err != nil {
		return err
	}
	free, err := l.FreeSpace(l.Root)
	if err != nil {
		return err
	}
	if free < uint64(n)+(128<<20) {
		return errors.New("资料盘空间不足，请至少保留 128 MiB 空闲空间")
	}
	return nil
}
func (l *Library) Setting(key string) ([]byte, error) {
	if err := l.Check(); err != nil {
		return nil, err
	}
	var value []byte
	err := l.db.QueryRow("SELECT value FROM settings WHERE key=?", key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return value, err
}
func (l *Library) SetSetting(key string, value []byte) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.Check(); err != nil {
		return err
	}
	_, err := l.db.Exec("INSERT INTO settings(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", key, value)
	return err
}
func (l *Library) Setup(hash []byte) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.Check(); err != nil {
		return err
	}
	_, err := l.db.Exec("INSERT INTO settings(key,value) VALUES('password',?)", hash)
	if err != nil {
		return ErrConflict
	}
	return nil
}
func (l *Library) ObjectPath(id string) string {
	if !IDPattern.MatchString(id) {
		return ""
	}
	return filepath.Join(l.Root, "objects", id)
}
func (l *Library) put(data []byte) (Page, error) {
	id := NewID()
	p := Page{Object: id, Size: int64(len(data))}
	hash := sha256.Sum256(data)
	p.SHA256 = hex.EncodeToString(hash[:])
	err := atomicfile.WriteNew(l.ObjectPath(id), func(w io.Writer) error { _, e := w.Write(data); return e })
	return p, err
}
func (l *Library) Items(module string, trash bool) ([]Item, error) {
	if !ValidModule(module) && module != "all" {
		return nil, errors.New("此模块尚未开放")
	}
	if err := l.Check(); err != nil {
		return nil, err
	}
	rows, err := l.db.Query("SELECT id,module,name,tags,created,deleted,revision,seq,pages,progress FROM items WHERE (?='all' OR module=?) AND (deleted!='')=? ORDER BY seq DESC", module, module, trash)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Item{}
	for rows.Next() {
		var it Item
		var tags, pages string
		if err = rows.Scan(&it.ID, &it.Module, &it.Name, &tags, &it.Created, &it.Deleted, &it.Revision, &it.Seq, &pages, &it.Progress); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(tags), &it.Tags); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(pages), &it.Pages); err != nil {
			return nil, err
		}
		items = append(items, it)
	}
	return items, rows.Err()
}
func (l *Library) Item(id string) (Item, error) {
	var it Item
	if err := l.Check(); err != nil {
		return it, err
	}
	var tags, pages string
	err := l.db.QueryRow("SELECT id,module,name,tags,created,deleted,revision,seq,pages,progress FROM items WHERE id=?", id).Scan(&it.ID, &it.Module, &it.Name, &tags, &it.Created, &it.Deleted, &it.Revision, &it.Seq, &pages, &it.Progress)
	if errors.Is(err, sql.ErrNoRows) {
		return it, ErrMissing
	}
	if err != nil {
		return it, err
	}
	if err = json.Unmarshal([]byte(tags), &it.Tags); err != nil {
		return it, err
	}
	err = json.Unmarshal([]byte(pages), &it.Pages)
	return it, err
}
func CleanName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 160 || strings.ContainsAny(name, "\x00\r\n") {
		return "", errors.New("名称需为 1–160 个字符，不能包含换行")
	}
	return name, nil
}
func CleanTags(tags []string) ([]string, error) {
	out := []string{}
	seen := map[string]bool{}
	for _, t := range tags {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		if len([]rune(t)) > 32 || len(out) >= 20 || strings.ContainsAny(t, "\x00\r\n") {
			return nil, errors.New("最多 20 个标签，每个不超过 32 字")
		}
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out, nil
}
func (l *Library) Update(id, name string, tags []string, revision int) error {
	name, err := CleanName(name)
	if err != nil {
		return err
	}
	tags, err = CleanTags(tags)
	if err != nil {
		return err
	}
	encoded, _ := json.Marshal(tags)
	l.mu.Lock()
	defer l.mu.Unlock()
	if err = l.Check(); err != nil {
		return err
	}
	result, err := l.db.Exec("UPDATE items SET name=?,tags=?,revision=revision+1 WHERE id=? AND module IN ('comics','novels') AND deleted='' AND revision=?", name, string(encoded), id, revision)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return ErrConflict
	}
	return nil
}
func (l *Library) Trash(id string, restore bool) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.Check(); err != nil {
		return err
	}
	date := ""
	if !restore {
		date = time.Now().UTC().Format(time.RFC3339Nano)
	}
	result, err := l.db.Exec("UPDATE items SET deleted=?,revision=revision+1 WHERE id=?", date, id)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return ErrMissing
	}
	return nil
}
func (l *Library) Progress(id string, page int) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	it, err := l.Item(id)
	if err != nil {
		return err
	}
	if it.Deleted != "" || page < 0 || page >= len(it.Pages) {
		return errors.New("无效页码")
	}
	_, err = l.db.Exec("UPDATE items SET progress=? WHERE id=?", page, id)
	return err
}

// Snapshot copies SQLite under the mutation lock, then pins immutable file
// versions with hard links. Object GC is serialized until all references are pinned.
func (l *Library) Snapshot(ctx context.Context, dest string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.NeedSpace(32 << 20); err != nil {
		return err
	}
	if err := os.Mkdir(dest, 0700); err != nil {
		return err
	}
	ok := false
	defer func() {
		if !ok {
			os.RemoveAll(dest)
		}
	}()
	if _, err := l.db.ExecContext(ctx, "VACUUM INTO ?", filepath.Join(dest, "index.db")); err != nil {
		return err
	}
	if err := os.Chmod(filepath.Join(dest, "index.db"), 0600); err != nil {
		return err
	}
	if err := l.recoveryManifest(dest); err != nil {
		return err
	}
	for _, name := range []string{"objects", "staging", "private-objects", "chunks"} {
		if err := os.Mkdir(filepath.Join(dest, name), 0700); err != nil {
			return err
		}
	}
	if err := atomicfile.CopyNew(filepath.Join(dest, "format"), filepath.Join(l.Root, "format")); err != nil {
		return err
	}
	items, err := l.Items("all", false)
	if err != nil {
		return err
	}
	trash, err := l.Items("all", true)
	if err != nil {
		return err
	}
	items = append(items, trash...)
	ids := map[string]bool{}
	for _, it := range items {
		for _, p := range it.Pages {
			ids[p.Object] = true
			if p.Thumb != "" {
				ids[p.Thumb] = true
			}
		}
	}
	uploads, err := l.Uploads()
	if err != nil {
		return err
	}
	for _, up := range uploads {
		if up.State == "cancelled" {
			continue
		}
		for _, f := range up.Files {
			if f.Page != nil {
				ids[f.Page.Object] = true
				if f.Page.Thumb != "" {
					ids[f.Page.Thumb] = true
				}
			}
		}
	}
	private, err := l.privateRows()
	if err != nil {
		return err
	}
	// Keep references protected until every immutable object is pinned.
	for _, row := range private {
		for _, id := range []string{row.object, row.thumb} {
			if id == "" {
				continue
			}
			if err = ctx.Err(); err != nil {
				return err
			}
			if !IDPattern.MatchString(id) {
				return errors.New("无效私密文件引用")
			}
			if err = os.Link(l.PrivatePath(id), filepath.Join(dest, "private-objects", id)); err != nil {
				return err
			}
		}
	}
	for id := range ids {
		if err = ctx.Err(); err != nil {
			return err
		}
		if !IDPattern.MatchString(id) {
			return errors.New("无效文件引用")
		}
		if err = os.Link(l.ObjectPath(id), filepath.Join(dest, "objects", id)); err != nil {
			return err
		}
	}
	rows, err := l.db.Query("SELECT object FROM transfer_chunks")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return err
		}
		if !IDPattern.MatchString(id) {
			return errors.New("无效分块引用")
		}
		if err = os.Link(filepath.Join(l.Root, "chunks", id), filepath.Join(dest, "chunks", id)); err != nil {
			return err
		}
	}
	if err = rows.Err(); err != nil {
		return err
	}
	ok = true
	return nil
}
func (l *Library) Verify(ctx context.Context) error {
	if err := l.Check(); err != nil {
		return err
	}
	var integrity string
	if err := l.db.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&integrity); err != nil {
		return err
	}
	if integrity != "ok" {
		return errors.New("资料索引完整性检查失败")
	}
	rows, err := l.db.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return err
	}
	bad := rows.Next()
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if bad {
		return errors.New("资料索引包含无效关联")
	}
	pages := map[string]Page{}
	for _, trash := range []bool{false, true} {
		items, err := l.Items("all", trash)
		if err != nil {
			return err
		}
		for _, it := range items {
			for _, p := range it.Pages {
				pages[p.Object] = p
			}
		}
	}
	uploads, err := l.Uploads()
	if err != nil {
		return err
	}
	for _, up := range uploads {
		if up.State == "cancelled" {
			continue
		}
		for _, f := range up.Files {
			if f.Page != nil {
				pages[f.Page.Object] = *f.Page
			}
		}
	}
	for _, p := range pages {
		if err = ctx.Err(); err != nil {
			return err
		}
		f, e := os.Open(l.ObjectPath(p.Object))
		if e != nil {
			return e
		}
		h := sha256.New()
		n, e := io.Copy(h, f)
		f.Close()
		if e != nil || n != p.Size || hex.EncodeToString(h.Sum(nil)) != p.SHA256 {
			return fmt.Errorf("文件校验失败：%s", p.Object)
		}
		if p.Thumb != "" {
			if _, e = os.Stat(l.ObjectPath(p.Thumb)); e != nil {
				return e
			}
		}
	}
	if err := l.verifyChunks(ctx); err != nil {
		return err
	}
	return l.verifyPrivate(ctx)
}
