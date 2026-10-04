package library

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/soraincloud/srics-next/internal/atomicfile"
	"github.com/soraincloud/srics-next/internal/vault"
)

const MaxOrdinaryFile int64 = 10 << 30

// Ordinary files use the existing immutable object store. Older readers must
// reject this module rather than overlook its references during collection.
func migrateFiles(db *sql.DB, root string, existing bool) error {
	if existing {
		path := filepath.Join(root, "staging", "before-files-"+NewID()+".db")
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
	_, err := db.Exec("PRAGMA user_version=6")
	return err
}

// Caller holds objectsMu and mu. Stream arbitrary originals without decoding,
// conversion or a full in-memory copy, and publish only after size/hash checks.
func (l *Library) receiveFile(ctx context.Context, f UploadFile, sourceHash string, src io.Reader) (Page, error) {
	p := Page{Name: f.Name, Object: NewID(), Size: f.Size, SHA256: sourceHash, MIME: "application/octet-stream"}
	if f.Size < 0 || f.Size > MaxOrdinaryFile {
		return p, errors.New("单文件最多 10 GiB")
	}
	if err := l.NeedSpace(f.Size + 1<<20); err != nil {
		return p, err
	}
	err := atomicfile.WriteNew(l.ObjectPath(p.Object), func(w io.Writer) error {
		h := sha256.New()
		n, err := io.Copy(io.MultiWriter(w, h), vault.ContextReader{Ctx: ctx, Reader: io.LimitReader(src, f.Size+1)})
		if err != nil {
			return err
		}
		if n != f.Size || hex.EncodeToString(h.Sum(nil)) != sourceHash {
			return errors.New("文件大小或内容校验失败，请选择原文件重试")
		}
		return ctx.Err()
	})
	return p, err
}
