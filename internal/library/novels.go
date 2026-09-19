package library

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

const MaxChapterBody = 512 << 10
const MaxChapters = 3000

type Chapter struct {
	ID       string `json:"id"`
	NovelID  string `json:"novelId"`
	Title    string `json:"title"`
	Body     string `json:"body,omitempty"`
	Position int    `json:"position"`
	Revision int    `json:"revision"`
	Deleted  string `json:"deleted"`
	Updated  string `json:"updated"`
}
type ChapterVersion struct {
	Revision int    `json:"revision"`
	Title    string `json:"title"`
	Body     string `json:"body,omitempty"`
	Saved    string `json:"saved"`
}
type Novel struct {
	Item     Item      `json:"item"`
	Chapters []Chapter `json:"chapters"`
	Trash    []Chapter `json:"trash"`
	Reading  string    `json:"reading"`
}

func migrateNovels(db *sql.DB, root string, existing bool) error {
	// Preserve a consistent pre-upgrade index before changing any existing schema.
	if existing {
		path := filepath.Join(root, "staging", "before-novels-"+NewID()+".db")
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		if err = f.Close(); err != nil {
			return err
		}
		if _, err = db.Exec("VACUUM INTO ?", path); err != nil {
			return err
		}
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.Exec(`CREATE TABLE chapters(
 id TEXT PRIMARY KEY, novel_id TEXT NOT NULL REFERENCES items(id),
 title TEXT NOT NULL, body TEXT NOT NULL, position INTEGER NOT NULL,
 revision INTEGER NOT NULL DEFAULT 1, deleted TEXT NOT NULL DEFAULT '', updated TEXT NOT NULL);
 CREATE INDEX chapters_novel ON chapters(novel_id,deleted,position);
 CREATE TABLE chapter_versions(
 chapter_id TEXT NOT NULL REFERENCES chapters(id), revision INTEGER NOT NULL,
 title TEXT NOT NULL, body TEXT NOT NULL, saved TEXT NOT NULL, automatic INTEGER NOT NULL,
 PRIMARY KEY(chapter_id,revision));
 CREATE TABLE novel_reading(
 novel_id TEXT PRIMARY KEY REFERENCES items(id), chapter_id TEXT NOT NULL REFERENCES chapters(id));
 PRAGMA user_version=2;`)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (l *Library) CreateNovel(id, name string, tags []string) (Item, error) {
	if !IDPattern.MatchString(id) {
		return Item{}, errors.New("无效小说编号")
	}
	name, err := CleanName(name)
	if err != nil {
		return Item{}, err
	}
	tags, err = CleanTags(tags)
	if err != nil {
		return Item{}, err
	}
	encoded, _ := json.Marshal(tags)
	l.mu.Lock()
	defer l.mu.Unlock()
	if err = l.NeedSpace(1 << 20); err != nil {
		return Item{}, err
	}
	old, err := l.Item(id)
	if err == nil {
		oldTags, _ := json.Marshal(old.Tags)
		if old.Module == "novels" && old.Deleted == "" && old.Name == name && string(oldTags) == string(encoded) {
			return old, nil
		}
		return Item{}, ErrConflict
	}
	if !errors.Is(err, ErrMissing) {
		return Item{}, err
	}
	_, err = l.db.Exec("INSERT INTO items(id,module,name,tags,created,pages) VALUES(?,'novels',?,?,?,'[]')", id, name, string(encoded), time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return Item{}, err
	}
	return l.Item(id)
}

// All novel reads use the same mutex as mutations so metadata, chapter order,
// reading position and exported text describe one consistent revision.
func (l *Library) Novel(id string) (Novel, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.novel(id)
}
func (l *Library) novel(id string) (Novel, error) {
	n := Novel{Chapters: []Chapter{}, Trash: []Chapter{}}
	it, err := l.Item(id)
	if err != nil {
		return n, err
	}
	if it.Module != "novels" || it.Deleted != "" {
		return n, ErrMissing
	}
	n.Item = it
	rows, err := l.db.Query("SELECT id,novel_id,title,position,revision,deleted,updated FROM chapters WHERE novel_id=? ORDER BY position,id", id)
	if err != nil {
		return n, err
	}
	for rows.Next() {
		var c Chapter
		if err = rows.Scan(&c.ID, &c.NovelID, &c.Title, &c.Position, &c.Revision, &c.Deleted, &c.Updated); err != nil {
			rows.Close()
			return n, err
		}
		if c.Deleted == "" {
			n.Chapters = append(n.Chapters, c)
		} else {
			n.Trash = append(n.Trash, c)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return n, err
	}
	err = l.db.QueryRow("SELECT chapter_id FROM novel_reading WHERE novel_id=?", id).Scan(&n.Reading)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	return n, err
}
func (l *Library) chapter(novel, id string) (Chapter, error) {
	var c Chapter
	err := l.db.QueryRow("SELECT c.id,c.novel_id,c.title,c.body,c.position,c.revision,c.deleted,c.updated FROM chapters c JOIN items i ON i.id=c.novel_id WHERE c.id=? AND c.novel_id=? AND i.module='novels' AND i.deleted=''", id, novel).Scan(&c.ID, &c.NovelID, &c.Title, &c.Body, &c.Position, &c.Revision, &c.Deleted, &c.Updated)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrMissing
	}
	return c, err
}
func (l *Library) Chapter(novel, id string) (Chapter, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.Check(); err != nil {
		return Chapter{}, err
	}
	return l.chapter(novel, id)
}
func cleanChapter(title, body string) (string, error) {
	title, err := CleanName(title)
	if err != nil {
		return "", err
	}
	if len(body) > MaxChapterBody || !utf8.ValidString(body) || strings.ContainsRune(body, 0) {
		return "", errors.New("每章正文最多 512 KiB UTF-8 文本，不能包含空字符")
	}
	return title, nil
}
func (l *Library) CreateChapter(novel, id, title, body string, revision int) (Chapter, error) {
	if !IDPattern.MatchString(id) {
		return Chapter{}, errors.New("无效章节编号")
	}
	title, err := cleanChapter(title, body)
	if err != nil {
		return Chapter{}, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err = l.NeedSpace(int64(len(body))*3 + 1<<20); err != nil {
		return Chapter{}, err
	}
	it, err := l.Item(novel)
	if err != nil {
		return Chapter{}, err
	}
	if it.Module != "novels" || it.Deleted != "" {
		return Chapter{}, ErrMissing
	}
	old, e := l.chapter(novel, id)
	if e == nil {
		if old.Deleted == "" && old.Title == title && old.Body == body {
			return old, nil
		}
		return Chapter{}, ErrConflict
	}
	if !errors.Is(e, ErrMissing) {
		return Chapter{}, e
	}
	if it.Revision != revision {
		return Chapter{}, ErrConflict
	}
	var count, position int
	if err = l.db.QueryRow("SELECT count(*),COALESCE(MAX(position),-1)+1 FROM chapters WHERE novel_id=?", novel).Scan(&count, &position); err != nil {
		return Chapter{}, err
	}
	if count >= MaxChapters {
		return Chapter{}, errors.New("每本小说最多 3000 章（含已删除章节）")
	}
	tx, err := l.db.Begin()
	if err != nil {
		return Chapter{}, err
	}
	defer tx.Rollback()
	_, err = tx.Exec("INSERT INTO chapters(id,novel_id,title,body,position,updated) VALUES(?,?,?,?,?,?)", id, novel, title, body, position, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return Chapter{}, err
	}
	if _, err = tx.Exec("UPDATE items SET revision=revision+1 WHERE id=?", novel); err != nil {
		return Chapter{}, err
	}
	if err = tx.Commit(); err != nil {
		return Chapter{}, err
	}
	return l.chapter(novel, id)
}

func checkpoint(tx *sql.Tx, c Chapter, automatic bool) error {
	now := time.Now().UTC()
	if automatic {
		var saved string
		var auto bool
		err := tx.QueryRow("SELECT saved,automatic FROM chapter_versions WHERE chapter_id=? ORDER BY revision DESC LIMIT 1", c.ID).Scan(&saved, &auto)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		when, _ := time.Parse(time.RFC3339Nano, saved)
		// Keep the state from the beginning of a five-minute automatic-save burst.
		if auto && now.Sub(when) < 5*time.Minute {
			return nil
		}
	}
	_, err := tx.Exec("INSERT INTO chapter_versions(chapter_id,revision,title,body,saved,automatic) VALUES(?,?,?,?,?,?)", c.ID, c.Revision, c.Title, c.Body, now.Format(time.RFC3339Nano), automatic)
	if err != nil {
		return err
	}
	_, err = tx.Exec("DELETE FROM chapter_versions WHERE chapter_id=? AND revision NOT IN (SELECT revision FROM chapter_versions WHERE chapter_id=? ORDER BY revision DESC LIMIT 20)", c.ID, c.ID)
	return err
}
func (l *Library) SaveChapter(novel, id, title, body string, revision int, automatic bool) (Chapter, error) {
	title, err := cleanChapter(title, body)
	if err != nil {
		return Chapter{}, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err = l.NeedSpace(int64(len(body))*3 + 1<<20); err != nil {
		return Chapter{}, err
	}
	old, err := l.chapter(novel, id)
	if err != nil {
		return Chapter{}, err
	}
	if old.Deleted != "" {
		return Chapter{}, ErrMissing
	}
	if old.Revision != revision {
		// A retried request after a lost response is harmless if it already succeeded.
		if old.Revision == revision+1 && old.Title == title && old.Body == body {
			return old, nil
		}
		return Chapter{}, ErrConflict
	}
	if old.Title == title && old.Body == body {
		return old, nil
	}
	return l.saveChapter(old, title, body, automatic)
}
func (l *Library) saveChapter(old Chapter, title, body string, automatic bool) (Chapter, error) {
	tx, err := l.db.Begin()
	if err != nil {
		return Chapter{}, err
	}
	defer tx.Rollback()
	if err = checkpoint(tx, old, automatic); err != nil {
		return Chapter{}, err
	}
	_, err = tx.Exec("UPDATE chapters SET title=?,body=?,revision=revision+1,updated=? WHERE id=?", title, body, time.Now().UTC().Format(time.RFC3339Nano), old.ID)
	if err != nil {
		return Chapter{}, err
	}
	if err = tx.Commit(); err != nil {
		return Chapter{}, err
	}
	return l.chapter(old.NovelID, old.ID)
}
func (l *Library) Versions(novel, id string) ([]ChapterVersion, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.Check(); err != nil {
		return nil, err
	}
	if _, err := l.chapter(novel, id); err != nil {
		return nil, err
	}
	rows, err := l.db.Query("SELECT revision,title,saved FROM chapter_versions WHERE chapter_id=? ORDER BY revision DESC", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ChapterVersion{}
	for rows.Next() {
		var v ChapterVersion
		if err = rows.Scan(&v.Revision, &v.Title, &v.Saved); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (l *Library) Version(novel, id string, revision int) (ChapterVersion, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.Check(); err != nil {
		return ChapterVersion{}, err
	}
	if _, err := l.chapter(novel, id); err != nil {
		return ChapterVersion{}, err
	}
	return l.version(id, revision)
}
func (l *Library) version(id string, revision int) (ChapterVersion, error) {
	var v ChapterVersion
	err := l.db.QueryRow("SELECT revision,title,body,saved FROM chapter_versions WHERE chapter_id=? AND revision=?", id, revision).Scan(&v.Revision, &v.Title, &v.Body, &v.Saved)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrMissing
	}
	return v, err
}
func (l *Library) RestoreVersion(novel, id string, revision, target int) (Chapter, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.NeedSpace(2 << 20); err != nil {
		return Chapter{}, err
	}
	c, err := l.chapter(novel, id)
	if err != nil {
		return Chapter{}, err
	}
	if c.Deleted != "" {
		return Chapter{}, ErrMissing
	}
	if c.Revision != revision {
		return Chapter{}, ErrConflict
	}
	v, err := l.version(id, target)
	if err != nil {
		return Chapter{}, err
	}
	return l.saveChapter(c, v.Title, v.Body, false)
}
func (l *Library) TrashChapter(novel, id string, revision int, restore bool) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.NeedSpace(1 << 20); err != nil {
		return err
	}
	c, err := l.chapter(novel, id)
	if err != nil {
		return err
	}
	if c.Revision != revision {
		return ErrConflict
	}
	if (c.Deleted == "") == restore {
		return nil
	}
	date := ""
	if !restore {
		date = time.Now().UTC().Format(time.RFC3339Nano)
	}
	tx, err := l.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec("UPDATE chapters SET deleted=?,revision=revision+1 WHERE id=?", date, id); err != nil {
		return err
	}
	if _, err = tx.Exec("UPDATE items SET revision=revision+1 WHERE id=?", novel); err != nil {
		return err
	}
	if !restore {
		if _, err = tx.Exec("DELETE FROM novel_reading WHERE novel_id=? AND chapter_id=?", novel, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func (l *Library) ReorderChapters(novel string, ids []string, revision int) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.NeedSpace(1 << 20); err != nil {
		return err
	}
	n, err := l.novel(novel)
	if err != nil {
		return err
	}
	if n.Item.Revision != revision {
		return ErrConflict
	}
	if len(ids) != len(n.Chapters) {
		return ErrConflict
	}
	remaining := map[string]bool{}
	for _, c := range n.Chapters {
		remaining[c.ID] = true
	}
	for _, id := range ids {
		if !remaining[id] {
			return errors.New("章节顺序必须包含每个有效章节且不能重复")
		}
		delete(remaining, id)
	}
	// Deleted chapters retain their relative insertion slot when restored.
	tx, err := l.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for i, id := range ids {
		if _, err = tx.Exec("UPDATE chapters SET position=? WHERE id=?", n.Chapters[i].Position, id); err != nil {
			return err
		}
	}
	if _, err = tx.Exec("UPDATE items SET revision=revision+1 WHERE id=?", novel); err != nil {
		return err
	}
	return tx.Commit()
}
func (l *Library) NovelProgress(novel, id string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.Check(); err != nil {
		return err
	}
	c, err := l.chapter(novel, id)
	if err != nil {
		return err
	}
	if c.Deleted != "" {
		return ErrMissing
	}
	_, err = l.db.Exec("INSERT INTO novel_reading(novel_id,chapter_id) VALUES(?,?) ON CONFLICT(novel_id) DO UPDATE SET chapter_id=excluded.chapter_id", novel, id)
	return err
}

// Export to a caller-owned temporary stream without loading all chapter bodies.
// The mutex only spans the local copy; HTTP delivery happens after it is released.
func (l *Library) ExportNovel(id string, out io.Writer) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	it, err := l.Item(id)
	if err != nil {
		return "", err
	}
	if it.Module != "novels" || it.Deleted != "" {
		return "", ErrMissing
	}
	var size int64
	if err = l.db.QueryRow("SELECT COALESCE(SUM(length(CAST(body AS BLOB))+length(CAST(title AS BLOB))+4),0) FROM chapters WHERE novel_id=? AND deleted=''", id).Scan(&size); err != nil {
		return "", err
	}
	if err = l.NeedSpace(size + int64(len(it.Name)) + 2); err != nil {
		return "", err
	}
	rows, err := l.db.Query("SELECT title,body FROM chapters WHERE novel_id=? AND deleted='' ORDER BY position,id", id)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	if _, err = io.WriteString(out, it.Name+"\n\n"); err != nil {
		return "", err
	}
	for rows.Next() {
		var title, body string
		if err = rows.Scan(&title, &body); err != nil {
			return "", err
		}
		if _, err = io.WriteString(out, title+"\n\n"+body+"\n\n"); err != nil {
			return "", err
		}
	}
	return it.Name, rows.Err()
}
