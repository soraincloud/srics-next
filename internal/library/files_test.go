package library

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/soraincloud/srics-next/internal/media"
)

func TestOrdinaryFileStreamingResumeSnapshotAndRename(t *testing.T) {
	l, ctx := testLibrary(t), context.Background()
	// Cross the image upload limit while using only one chunk of source memory.
	size := int64(MaxFile) + 17
	chunk := bytes.Repeat([]byte{0xa7}, int(ChunkSize))
	up, err := l.CreateUpload(Upload{ID: NewID(), Module: "attachments", Name: "资料原件", Files: []UploadFile{{Name: ".archive.bin", Size: size}}})
	if err != nil {
		t.Fatal(err)
	}
	tr := Transfer{ID: NewID(), Parent: up.ID, Module: up.Module, Name: up.Files[0].Name, Size: size}
	h := sha256.New()
	for start := int64(0); start < size; start += ChunkSize {
		b := chunk[:min(ChunkSize, size-start)]
		tr.Hashes = append(tr.Hashes, hash(b))
		h.Write(b)
	}
	if _, err = l.CreateTransfer(ctx, tr, nil); err != nil {
		t.Fatal(err)
	}
	if _, err = l.ReceiveChunk(ctx, tr.ID, 0, bytes.NewReader(chunk), nil); err != nil {
		t.Fatal(err)
	}
	root := l.Root
	l.Close()
	l, err = Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	resumed, err := l.CreateTransfer(ctx, tr, nil)
	if err != nil || len(resumed.Done) != 1 {
		t.Fatal("restart lost saved chunk", resumed, err)
	}
	for i := 1; i < len(tr.Hashes); i++ {
		b := chunk[:min(ChunkSize, size-int64(i)*ChunkSize)]
		if _, err = l.ReceiveChunk(ctx, tr.ID, i, bytes.NewReader(b), nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = l.FinishTransfer(ctx, tr.ID, nil, media.Converter{}); err != nil {
		t.Fatal(err)
	}
	it, err := l.Finish(up.ID)
	if err != nil || it.Pages[0].Size != size || it.Pages[0].SHA256 != hex.EncodeToString(h.Sum(nil)) || it.Pages[0].Thumb != "" {
		t.Fatal("original changed", it, err)
	}
	if err = l.Update(it.ID, "重命名资料", nil, it.Revision); err != nil {
		t.Fatal(err)
	}
	if err = l.Update(it.ID, "过期名称", nil, it.Revision); !errors.Is(err, ErrConflict) {
		t.Fatal("stale rename accepted", err)
	}
	if err = l.Trash(it.ID, false); err != nil {
		t.Fatal(err)
	}
	stage := filepath.Join(t.TempDir(), "snapshot")
	if err = l.Snapshot(ctx, stage); err != nil {
		t.Fatal(err)
	}
	if err = l.Purge(it.ID); err != nil {
		t.Fatal(err)
	}
	pinned, err := Open(stage)
	if err != nil {
		t.Fatal(err)
	}
	defer pinned.Close()
	if err = pinned.Trash(it.ID, true); err != nil {
		t.Fatal(err)
	}
	restored, err := pinned.Item(it.ID)
	if err != nil || restored.Name != "重命名资料" || restored.Pages[0].Name != ".archive.bin" {
		t.Fatal("snapshot lost metadata", restored, err)
	}
	got := sha256.New()
	if err = pinned.CopyOriginal(ctx, restored.Pages[0], got); err != nil || !bytes.Equal(got.Sum(nil), h.Sum(nil)) {
		t.Fatal("snapshot lost original", err)
	}
	if err = pinned.Verify(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestOrdinaryFileEmptyCorruptionAndFailure(t *testing.T) {
	l, ctx := testLibrary(t), context.Background()
	up, err := l.CreateUpload(Upload{ID: NewID(), Module: "attachments", Name: "空文件", Files: []UploadFile{{Name: "empty.txt", Size: 0}}})
	if err != nil {
		t.Fatal(err)
	}
	tr := Transfer{ID: NewID(), Parent: up.ID, Module: up.Module, Name: "empty.txt", Size: 0, Hashes: []string{}}
	if _, err = l.CreateTransfer(ctx, tr, nil); err != nil {
		t.Fatal(err)
	}
	if _, err = l.FinishTransfer(ctx, tr.ID, nil, media.Converter{}); err != nil {
		t.Fatal(err)
	}
	it, err := l.Finish(up.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = l.Verify(ctx); err != nil {
		t.Fatal("empty file could not be backed up", err)
	}
	for _, f := range []UploadFile{{Name: "../bad", Size: 1}, {Name: "..", Size: 1}, {Name: "large", Size: MaxOrdinaryFile + 1}} {
		if _, err = l.CreateUpload(Upload{ID: NewID(), Module: "attachments", Name: "invalid", Files: []UploadFile{f}}); err == nil {
			t.Fatal("invalid file accepted", f)
		}
	}
	up, err = l.CreateUpload(Upload{ID: NewID(), Module: "attachments", Name: "HTML 原件", Files: []UploadFile{{Name: "example.html", Size: 3}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = l.Receive(ctx, up.ID, 0, hash([]byte("abc")), bytes.NewReader([]byte("xyz")), media.Converter{}); err == nil {
		t.Fatal("hash mismatch accepted")
	}
	if _, err = l.Finish(up.ID); err == nil {
		t.Fatal("failed file published")
	}
	if err = l.Collect(); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(filepath.Join(l.Root, "objects"))
	if len(entries) != 1 {
		t.Fatal("failed write left a published object", len(entries))
	}
	if _, err = l.Receive(ctx, up.ID, 0, hash([]byte("abc")), bytes.NewReader([]byte("abc")), media.Converter{}); err != nil {
		t.Fatal(err)
	}
	actual, err := l.Finish(up.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(l.ObjectPath(actual.Pages[0].Object), []byte("xyz"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = l.OpenOriginal(ctx, actual.Pages[0]); err == nil {
		t.Fatal("corrupt file delivered")
	}
	if err = l.CopyOriginal(ctx, it.Pages[0], io.Discard); err != nil {
		t.Fatal("empty original changed", err)
	}
}

func TestOrdinaryFileUpgradePreservesVersionFiveLibrary(t *testing.T) {
	l := testLibrary(t)
	oldItem := upload(t, l, "photos", "original.png", fixture(t))
	if _, err := l.db.Exec("PRAGMA user_version=5"); err != nil {
		t.Fatal(err)
	}
	root := l.Root
	l.Close()
	l, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if _, err = l.Item(oldItem.ID); err != nil {
		t.Fatal("upgrade lost existing content", err)
	}
	paths, _ := filepath.Glob(filepath.Join(root, "staging", "before-files-*.db"))
	if len(paths) != 1 {
		t.Fatal("missing pre-upgrade index")
	}
	db, err := sql.Open("sqlite3", paths[0]+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var version int
	if err = db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 5 {
		t.Fatal("pre-upgrade index changed", version, err)
	}
}
