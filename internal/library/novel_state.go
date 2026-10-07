package library

import (
	"database/sql"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func migrateNovelState(db *sql.DB, root string, existing bool) error {
	if existing {
		path := filepath.Join(root, "staging", "before-novel-state-"+NewID()+".db")
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
	_, err = tx.Exec(`CREATE TABLE novel_state(
 novel_id TEXT PRIMARY KEY REFERENCES items(id),
 completed INTEGER NOT NULL DEFAULT 0 CHECK(completed IN (0,1)),
 paragraph INTEGER NOT NULL DEFAULT 0 CHECK(paragraph>=0),
 fraction REAL NOT NULL DEFAULT 0 CHECK(fraction>=0 AND fraction<=1),
 reading_revision INTEGER NOT NULL DEFAULT 0,
 reading_updated TEXT NOT NULL DEFAULT '');
 PRAGMA user_version=7;`)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (l *Library) SetNovelCompleted(id string, completed bool, revision int) (Item, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	it, err := l.Item(id)
	if err != nil {
		return Item{}, err
	}
	if it.Module != "novels" || it.Deleted != "" {
		return Item{}, ErrMissing
	}
	// A repeated request after a lost response must be safe, but an opposite
	// change from a stale page must never overwrite a newer status.
	if it.Completed == completed {
		return it, nil
	}
	if it.Revision != revision {
		return Item{}, ErrConflict
	}
	if err = l.NeedSpace(1 << 20); err != nil {
		return Item{}, err
	}
	tx, err := l.db.Begin()
	if err != nil {
		return Item{}, err
	}
	defer tx.Rollback()
	if _, err = tx.Exec("INSERT INTO novel_state(novel_id,completed) VALUES(?,?) ON CONFLICT(novel_id) DO UPDATE SET completed=excluded.completed", id, completed); err != nil {
		return Item{}, err
	}
	if _, err = tx.Exec("UPDATE items SET revision=revision+1 WHERE id=?", id); err != nil {
		return Item{}, err
	}
	if err = tx.Commit(); err != nil {
		return Item{}, err
	}
	return l.Item(id)
}

func (l *Library) SaveNovelBookmark(novel string, b NovelBookmark) error {
	if b.Paragraph < 0 || b.Fraction < 0 || b.Fraction > 1 || math.IsNaN(b.Fraction) || math.IsInf(b.Fraction, 0) || b.Revision < 0 {
		return errors.New("无效阅读位置")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.Check(); err != nil {
		return err
	}
	c, err := l.chapter(novel, b.Chapter)
	if err != nil {
		return err
	}
	if c.Deleted != "" {
		return ErrMissing
	}
	if b.Revision != 0 && b.Revision != c.Revision {
		return ErrConflict
	}
	if b.Paragraph > strings.Count(c.Body, "\n") {
		return errors.New("阅读位置超出章节正文")
	}
	if err = l.NeedSpace(1 << 20); err != nil {
		return err
	}
	tx, err := l.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec("INSERT INTO novel_reading(novel_id,chapter_id) VALUES(?,?) ON CONFLICT(novel_id) DO UPDATE SET chapter_id=excluded.chapter_id", novel, b.Chapter); err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO novel_state(novel_id,paragraph,fraction,reading_revision,reading_updated) VALUES(?,?,?,?,?)
 ON CONFLICT(novel_id) DO UPDATE SET paragraph=excluded.paragraph,fraction=excluded.fraction,reading_revision=excluded.reading_revision,reading_updated=excluded.reading_updated`, novel, b.Paragraph, b.Fraction, c.Revision, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	return tx.Commit()
}
