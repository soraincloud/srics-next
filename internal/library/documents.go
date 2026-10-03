package library

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

const MaxDocumentBody = 512 << 10

type Document struct {
	Item Item   `json:"item"`
	Body string `json:"body"`
}

// The storage tables stay the same. Bump the format so older programs cannot
// open a library containing document references they do not understand.
func migrateDocuments(db *sql.DB, root string, existing bool) error {
	if existing {
		path := filepath.Join(root, "staging", "before-documents-"+NewID()+".db")
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
	_, err := db.Exec("PRAGMA user_version=5")
	return err
}

func cleanDocument(name string, tags []string, body string) (string, []string, error) {
	name, err := CleanName(name)
	if err != nil {
		return "", nil, err
	}
	tags, err = CleanTags(tags)
	if err != nil {
		return "", nil, err
	}
	if !utf8.ValidString(body) || strings.ContainsRune(body, '\x00') || len(body) > MaxDocumentBody {
		return "", nil, errors.New("文档正文需为有效 UTF-8 文本，不能含空字符，最多 512 KiB")
	}
	return name, tags, nil
}

func (l *Library) Document(ctx context.Context, id string) (Document, error) {
	l.objectsMu.RLock()
	defer l.objectsMu.RUnlock()
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.document(ctx, id)
}

// Caller holds objectsMu and mu, in that order, for a consistent verified read.
func (l *Library) document(ctx context.Context, id string) (Document, error) {
	it, err := l.Item(id)
	if err != nil {
		return Document{}, err
	}
	if it.Module != "documents" || it.Deleted != "" {
		return Document{}, ErrMissing
	}
	f, err := l.openObject(it.Pages[0].Object)
	if err != nil {
		return Document{}, err
	}
	defer f.Close()
	var body bytes.Buffer
	if err = checkOriginal(ctx, it.Pages[0], f, &body); err != nil {
		return Document{}, err
	}
	if _, _, err = cleanDocument(it.Name, it.Tags, body.String()); err != nil {
		return Document{}, err
	}
	return Document{Item: it, Body: body.String()}, nil
}

// Revision 0 creates; later revisions update. Repeating an identical request
// after a lost response is safe. A stale, different edit is never overwritten.
func (l *Library) SaveDocument(ctx context.Context, id, name string, tags []string, body string, revision int) (Document, error) {
	if !IDPattern.MatchString(id) || revision < 0 {
		return Document{}, errors.New("无效文档编号或版本")
	}
	name, tags, err := cleanDocument(name, tags, body)
	if err != nil {
		return Document{}, err
	}
	encoded, _ := json.Marshal(tags)
	l.objectsMu.RLock()
	defer l.objectsMu.RUnlock()
	l.mu.Lock()
	defer l.mu.Unlock()
	if err = l.NeedSpace(int64(len(body)) + (1 << 20)); err != nil {
		return Document{}, err
	}
	if err = ctx.Err(); err != nil {
		return Document{}, err
	}
	old, err := l.document(ctx, id)
	if err == nil {
		oldTags, _ := json.Marshal(old.Item.Tags)
		same := name == old.Item.Name && bytes.Equal(oldTags, encoded) && body == old.Body
		if same && (old.Item.Revision == revision || old.Item.Revision == revision+1) {
			return old, nil
		}
		if revision == 0 || old.Item.Revision != revision {
			return Document{}, ErrConflict
		}
	} else {
		if !errors.Is(err, ErrMissing) {
			return Document{}, err
		}
		if revision != 0 {
			return Document{}, ErrMissing
		}
		// A deleted document or an ID belonging to another module cannot be reused.
		if _, e := l.Item(id); !errors.Is(e, ErrMissing) {
			if e != nil {
				return Document{}, e
			}
			return Document{}, ErrConflict
		}
	}
	p := Page{}
	if revision != 0 && old.Body == body {
		p = old.Item.Pages[0]
	} else {
		p, err = l.put([]byte(body))
		if err != nil {
			return Document{}, err
		}
		p.Name, p.MIME = "document.md", "text/markdown"
	}
	pages, _ := json.Marshal([]Page{p})
	// Publish only after the immutable body is durable. Unpublished objects are
	// left for collection if SQLite fails; do not risk deleting a committed file.
	if revision == 0 {
		_, err = l.db.ExecContext(ctx, "INSERT INTO items(id,module,name,tags,created,pages) VALUES(?,'documents',?,?,?,?)", id, name, string(encoded), time.Now().UTC().Format(time.RFC3339Nano), string(pages))
	} else {
		var result sql.Result
		result, err = l.db.ExecContext(ctx, "UPDATE items SET name=?,tags=?,pages=?,revision=revision+1 WHERE id=? AND module='documents' AND deleted='' AND revision=?", name, string(encoded), string(pages), id, revision)
		if err == nil {
			var n int64
			n, err = result.RowsAffected()
			if err == nil && n != 1 {
				err = ErrConflict
			}
		}
	}
	if err != nil {
		return Document{}, err
	}
	it, err := l.Item(id)
	return Document{Item: it, Body: body}, err
}
