package library

import (
	"context"
	"database/sql"
	"errors"
	"github.com/soraincloud/srics-next/internal/vault"
	"os"
	"path/filepath"
	"time"
)

type StorageUsage struct {
	Bytes int64  `json:"bytes"`
	Files int    `json:"files"`
	Free  uint64 `json:"free"`
}

func (l *Library) StorageUsage() (StorageUsage, error) {
	var out StorageUsage
	if err := l.Check(); err != nil {
		return out, err
	}
	free, err := l.FreeSpace(l.Root)
	if err != nil {
		return out, err
	}
	out.Free = free
	for _, dir := range []string{"objects", "private-objects", "chunks"} {
		entries, err := os.ReadDir(filepath.Join(l.Root, dir))
		if err != nil {
			return out, err
		}
		for _, entry := range entries {
			if !IDPattern.MatchString(entry.Name()) {
				continue
			}
			info, err := entry.Info()
			if err != nil {
				return out, err
			}
			out.Bytes += info.Size()
			out.Files++
		}
	}
	return out, nil
}
func (l *Library) collectLocked() error {
	if err := l.Check(); err != nil {
		return err
	}
	refs := map[string]map[string]bool{"objects": {}, "private-objects": {}, "chunks": {}}
	for _, trash := range []bool{false, true} {
		items, err := l.Items("all", trash)
		if err != nil {
			return err
		}
		for _, it := range items {
			for _, p := range it.Pages {
				refs["objects"][p.Object] = true
				refs["objects"][p.Thumb] = true
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
				refs["objects"][f.Page.Object] = true
				refs["objects"][f.Page.Thumb] = true
			}
		}
	}
	private, err := l.privateRows()
	if err != nil {
		return err
	}
	for _, r := range private {
		refs["private-objects"][r.object] = true
		refs["private-objects"][r.thumb] = true
	}
	rows, err := l.db.Query("SELECT object FROM transfer_chunks")
	if err != nil {
		return err
	}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			break
		}
		refs["chunks"][id] = true
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	for dir, keep := range refs {
		entries, err := os.ReadDir(filepath.Join(l.Root, dir))
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if IDPattern.MatchString(entry.Name()) && !keep[entry.Name()] {
				if err = l.Check(); err != nil {
					return err
				}
				if err = os.Remove(filepath.Join(l.Root, dir, entry.Name())); err != nil && !os.IsNotExist(err) {
					return err
				}
			}
		}
	}
	return nil
}
func (l *Library) Collect() error {
	l.transferMu.Lock()
	defer l.transferMu.Unlock()
	l.objectsMu.Lock()
	defer l.objectsMu.Unlock()
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.collectLocked()
}
func deleteChapter(tx *sql.Tx, id string) error {
	for _, q := range []string{"DELETE FROM novel_reading WHERE chapter_id=?", "DELETE FROM chapter_versions WHERE chapter_id=?", "DELETE FROM chapters WHERE id=?"} {
		if _, err := tx.Exec(q, id); err != nil {
			return err
		}
	}
	return nil
}
func (l *Library) purgeItemLocked(id string) error {
	it, err := l.Item(id)
	if err != nil {
		return err
	}
	if it.Deleted == "" {
		return errors.New("仅可彻底删除回收站中的资料")
	}
	tx, err := l.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec("DELETE FROM novel_reading WHERE novel_id=?", id); err != nil {
		return err
	}
	if _, err = tx.Exec("DELETE FROM chapter_versions WHERE chapter_id IN (SELECT id FROM chapters WHERE novel_id=?)", id); err != nil {
		return err
	}
	if _, err = tx.Exec("DELETE FROM chapters WHERE novel_id=?", id); err != nil {
		return err
	}
	if _, err = tx.Exec("DELETE FROM uploads WHERE id=?", id); err != nil {
		return err
	}
	if _, err = tx.Exec("DELETE FROM transfers WHERE private=0 AND json_extract(payload,'$.parent')=?", id); err != nil {
		return err
	}
	if _, err = tx.Exec("DELETE FROM items WHERE id=?", id); err != nil {
		return err
	}
	return tx.Commit()
}
func (l *Library) Purge(id string) error {
	l.transferMu.Lock()
	defer l.transferMu.Unlock()
	l.objectsMu.Lock()
	defer l.objectsMu.Unlock()
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.purgeItemLocked(id); err != nil {
		return err
	}
	return l.collectLocked()
}
func (l *Library) PurgeChapter(novel, id string, revision int) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.Check(); err != nil {
		return err
	}
	var deleted string
	var rev int
	if err := l.db.QueryRow("SELECT deleted,revision FROM chapters WHERE id=? AND novel_id=?", id, novel).Scan(&deleted, &rev); err != nil {
		return err
	}
	if deleted == "" || rev != revision {
		return ErrConflict
	}
	tx, err := l.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = deleteChapter(tx, id); err != nil {
		return err
	}
	return tx.Commit()
}
func (l *Library) PurgePrivate(ctx context.Context, a *vault.Access, id string, revision int) error {
	l.transferMu.Lock()
	defer l.transferMu.Unlock()
	l.objectsMu.Lock()
	defer l.objectsMu.Unlock()
	l.mu.Lock()
	defer l.mu.Unlock()
	it, err := l.PrivateItem(a, id)
	if err != nil {
		return err
	}
	if it.Deleted == "" || it.Revision != revision {
		return ErrConflict
	}
	err = a.Commit(ctx, func() error {
		if err := l.Check(); err != nil {
			return err
		}
		tx, err := l.db.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if _, err = tx.Exec("DELETE FROM private_items WHERE id=?", id); err != nil {
			return err
		}
		if _, err = tx.Exec("DELETE FROM transfers WHERE id=?", id); err != nil {
			return err
		}
		return tx.Commit()
	})
	if err != nil {
		return err
	}
	return l.collectLocked()
}
func (l *Library) ExpireTrash(ctx context.Context, days int) error {
	if days < 1 {
		return nil
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -days)
	l.transferMu.Lock()
	defer l.transferMu.Unlock()
	l.objectsMu.Lock()
	defer l.objectsMu.Unlock()
	l.mu.Lock()
	defer l.mu.Unlock()
	items, err := l.Items("all", true)
	if err != nil {
		return err
	}
	for _, it := range items {
		if err = ctx.Err(); err != nil {
			return err
		}
		date, e := time.Parse(time.RFC3339Nano, it.Deleted)
		if e != nil {
			return e
		}
		if date.Before(cutoff) {
			if err = l.purgeItemLocked(it.ID); err != nil {
				return err
			}
		}
	}
	rows, err := l.db.Query("SELECT id,deleted FROM chapters WHERE deleted!=''")
	if err != nil {
		return err
	}
	ids := []string{}
	for rows.Next() {
		var id, date string
		if err = rows.Scan(&id, &date); err != nil {
			break
		}
		d, e := time.Parse(time.RFC3339Nano, date)
		if e != nil {
			err = e
			break
		}
		if d.Before(cutoff) {
			ids = append(ids, id)
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	tx, err := l.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, id := range ids {
		if err = deleteChapter(tx, id); err != nil {
			return err
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	return l.collectLocked()
}
func (l *Library) ExpirePrivateTrash(ctx context.Context, a *vault.Access, days int) error {
	if days < 1 {
		return nil
	}
	l.transferMu.Lock()
	defer l.transferMu.Unlock()
	l.objectsMu.Lock()
	defer l.objectsMu.Unlock()
	l.mu.Lock()
	defer l.mu.Unlock()
	items, err := l.PrivateItems(ctx, a)
	if err != nil {
		return err
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -days)
	ids := []string{}
	for _, it := range items {
		if it.Deleted == "" {
			continue
		}
		date, err := time.Parse(time.RFC3339Nano, it.Deleted)
		if err != nil {
			return err
		}
		if date.Before(cutoff) {
			ids = append(ids, it.ID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	err = a.Commit(ctx, func() error {
		if err := l.Check(); err != nil {
			return err
		}
		tx, err := l.db.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()
		for _, id := range ids {
			if err = ctx.Err(); err != nil {
				return err
			}
			if _, err = tx.Exec("DELETE FROM private_items WHERE id=?", id); err != nil {
				return err
			}
			if _, err = tx.Exec("DELETE FROM transfers WHERE id=?", id); err != nil {
				return err
			}
		}
		return tx.Commit()
	})
	if err != nil {
		return err
	}
	// One sweep per batch, avoiding a full directory scan for each expired item.
	return l.collectLocked()
}
