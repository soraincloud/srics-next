package library

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"math"
	"os"

	"github.com/soraincloud/srics-next/internal/vault"
)

func validHash(hash string) bool {
	b, err := hex.DecodeString(hash)
	return err == nil && len(b) == sha256.Size && hex.EncodeToString(b) == hash
}

func (p Page) validate() error {
	// Lossless WebP can be larger than the uploaded JPEG; do not apply the
	// upload size limit to already-published originals.
	if p.Name == "" || !IDPattern.MatchString(p.Object) || p.Size < 0 || (p.Size == 0 && p.MIME != "text/markdown") || p.Size == math.MaxInt64 || !validHash(p.SHA256) || (p.Thumb != "" && !IDPattern.MatchString(p.Thumb)) {
		return errors.New("文件索引损坏，已停止读取和清理，请从备份恢复")
	}
	return nil
}

func (it Item) validate() error {
	if !IDPattern.MatchString(it.ID) || !ValidModule(it.Module) || it.Revision < 1 || (it.Module == "novels" && len(it.Pages) != 0) || (it.Module == "comics" && (len(it.Pages) < 1 || len(it.Pages) > 3000)) || ((it.Module == "images" || it.Module == "photos") && len(it.Pages) != 1) {
		return errors.New("资料索引损坏，已停止读取和清理，请从备份恢复")
	}
	for _, p := range it.Pages {
		if err := p.validate(); err != nil {
			return err
		}
		if p.Size == 0 && it.Module != "documents" {
			return errors.New("原件为空，请从备份恢复")
		}
	}
	if it.Module == "documents" && (len(it.Pages) != 1 || it.Pages[0].MIME != "text/markdown" || it.Pages[0].Size > MaxDocumentBody || it.Pages[0].Thumb != "") {
		return errors.New("文档索引损坏，请从备份恢复")
	}
	return nil
}

// OpenObject pins a regular immutable object before garbage collection can
// unlink it. A rename or unlink after this point cannot replace the open file.
func (l *Library) OpenObject(id string) (*os.File, error) {
	l.objectsMu.RLock()
	defer l.objectsMu.RUnlock()
	return l.openObject(id)
}

// Caller holds objectsMu while opening a reference that collection may remove.
func (l *Library) openObject(id string) (*os.File, error) {
	if err := l.Check(); err != nil {
		return nil, err
	}
	path := l.ObjectPath(id)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("文件缺失或不是普通文件，请检查资料盘")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	opened, err := f.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		f.Close()
		return nil, errors.New("文件在读取时发生变化")
	}
	return f, nil
}

func checkOriginal(ctx context.Context, p Page, f *os.File, out io.Writer) error {
	if err := p.validate(); err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil || info.Size() != p.Size {
		return errors.New("原件大小校验失败，请检查资料盘并从备份恢复")
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(out, h), vault.ContextReader{Ctx: ctx, Reader: io.LimitReader(f, p.Size+1)})
	if err != nil {
		return err
	}
	if n != p.Size || hex.EncodeToString(h.Sum(nil)) != p.SHA256 {
		return errors.New("原件内容校验失败，请检查资料盘并从备份恢复")
	}
	return nil
}

// OpenOriginal verifies the complete original before returning any bytes,
// including for range requests. No unbounded in-memory copy is needed.
func (l *Library) OpenOriginal(ctx context.Context, p Page) (*os.File, error) {
	f, err := l.OpenObject(p.Object)
	if err != nil {
		return nil, err
	}
	if err = checkOriginal(ctx, p, f, io.Discard); err == nil {
		_, err = f.Seek(0, io.SeekStart)
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

// CopyOriginal validates the same bytes it writes. The caller must discard an
// incomplete export (or abort an HTTP stream) on any error, never finalize it.
func (l *Library) CopyOriginal(ctx context.Context, p Page, out io.Writer) error {
	f, err := l.OpenObject(p.Object)
	if err != nil {
		return err
	}
	defer f.Close()
	return checkOriginal(ctx, p, f, out)
}
