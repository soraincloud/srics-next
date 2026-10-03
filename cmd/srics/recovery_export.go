package main

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/soraincloud/srics-next/internal/atomicfile"
	"github.com/soraincloud/srics-next/internal/library"
	"github.com/soraincloud/srics-next/internal/vault"
)

type recoveryItem struct {
	ID      string `json:"id"`
	Module  string `json:"module"`
	Name    string `json:"name"`
	Private bool   `json:"private"`
}

func recoveryAccess(ctx context.Context, l *library.Library, password string) (*vault.Access, error) {
	if password == "" {
		return nil, nil
	}
	wrapped, err := l.Setting("vault-key")
	if err != nil {
		return nil, err
	}
	key, err := vault.Unlock(wrapped, password)
	if err != nil {
		return nil, errors.New("保险库口令不正确或密钥损坏")
	}
	return vault.NewAccess(ctx, key, 24*time.Hour), nil
}
func recoveryItems(ctx context.Context, l *library.Library, a *vault.Access) ([]recoveryItem, error) {
	items, err := l.Items("all", false)
	if err != nil {
		return nil, err
	}
	out := make([]recoveryItem, 0, len(items))
	for _, it := range items {
		name := it.Name
		if name == "" && len(it.Pages) > 0 {
			name = it.Pages[0].Name
		}
		out = append(out, recoveryItem{it.ID, it.Module, name, false})
	}
	if a != nil {
		items, err := l.PrivateItems(ctx, a)
		if err != nil {
			return nil, err
		}
		for _, it := range items {
			if it.Deleted == "" {
				out = append(out, recoveryItem{it.ID, it.Module, it.Name, true})
			}
		}
	}
	return out, nil
}

// An export is an explicit plaintext copy into a new destination chosen on this Mac.
// No data is imported into the active library and existing files are never replaced.
func exportRecovery(ctx context.Context, l *library.Library, a *vault.Access, id string, private bool, destination string) (string, error) {
	if !library.IDPattern.MatchString(id) {
		return "", library.ErrMissing
	}
	name := ""
	var write func(io.Writer) error
	if private {
		if a == nil {
			return "", vault.ErrLocked
		}
		it, err := l.PrivateItem(a, id)
		if err != nil {
			return "", err
		}
		if it.Deleted != "" {
			return "", library.ErrMissing
		}
		name = it.Original
		write = func(w io.Writer) error {
			r, _, err := l.PrivateRead(ctx, a, it, false)
			if err != nil {
				return err
			}
			defer r.Close()
			_, err = io.Copy(w, r)
			return err
		}
	} else {
		it, err := l.Item(id)
		if err != nil {
			return "", err
		}
		if it.Deleted != "" {
			return "", library.ErrMissing
		}
		name = it.Name
		copyPage := func(w io.Writer, p library.Page) error {
			return l.CopyOriginal(ctx, p, w)
		}
		switch it.Module {
		case "documents":
			name += ".md"
			write = func(w io.Writer) error { return copyPage(w, it.Pages[0]) }
		case "novels":
			name += ".txt"
			write = func(w io.Writer) error {
				_, err := l.ExportNovel(id, vault.ContextWriter{Ctx: ctx, Writer: w})
				return err
			}
		case "comics":
			name += ".zip"
			write = func(w io.Writer) error {
				z := zip.NewWriter(w)
				for i, p := range it.Pages {
					f, err := z.CreateHeader(&zip.FileHeader{Name: fmt.Sprintf("%05d.webp", i+1), Method: zip.Store})
					if err != nil {
						return err
					}
					if err = copyPage(f, p); err != nil {
						return err
					}
				}
				return z.Close()
			}
		default:
			if len(it.Pages) != 1 {
				return "", errors.New("图片引用无效")
			}
			name = it.Pages[0].Name
			write = func(w io.Writer) error { return copyPage(w, it.Pages[0]) }
		}
	}
	name = strings.TrimSpace(strings.ReplaceAll(filepath.Base(name), "\\", "_"))
	if name == "" || name == "." || name == ".." {
		name = id
	}
	// Prefix with a random identifier, preserving the useful original extension.
	dest := filepath.Join(destination, library.NewID()[:8]+"-"+name)
	err := atomicfile.WriteNew(dest, func(w io.Writer) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := write(w); err != nil {
			return err
		}
		return ctx.Err()
	})
	return dest, err
}
func recoveryExportRequest(ctx context.Context, path string, req recoveryRequest) (recoveryResponse, error) {
	var out recoveryResponse
	c, _, err := loadConfig(path)
	if err != nil {
		return out, err
	}
	dest, err := recoveryDirectory(req.Directory, c.Data, c.BackupRepository, filepath.Dir(path))
	if err != nil {
		return out, err
	}
	_, l, err := checkedRecovery(ctx, dest)
	if err != nil {
		return out, err
	}
	defer l.Close()
	var a *vault.Access
	if req.Source.RecoveryKeyFile != "" {
		a, err = recoveryKeyAccess(ctx, l, req.Source.RecoveryKeyFile)
	} else {
		a, err = recoveryAccess(ctx, l, req.VaultPassword)
	}
	if err != nil {
		return out, err
	}
	if a != nil {
		defer a.Lock()
	}
	if req.ItemID == "" {
		out.Items, err = recoveryItems(ctx, l, a)
		return out, err
	}
	exportDir, err := recoveryDirectory(req.ExportDirectory, c.Data, c.BackupRepository, filepath.Dir(path), dest)
	if err != nil {
		return out, err
	}
	info, err := os.Stat(exportDir)
	if err != nil || !info.IsDir() {
		return out, errors.New("请选择已存在的导出目录")
	}
	out.Exported, err = exportRecovery(ctx, l, a, req.ItemID, req.Private, exportDir)
	return out, err
}
