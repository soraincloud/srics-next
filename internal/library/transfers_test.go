package library

import (
	"bytes"
	"context"
	"errors"
	"github.com/soraincloud/srics-next/internal/media"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func transferFixture(t *testing.T, l *Library, data []byte) Transfer {
	t.Helper()
	up, err := l.CreateUpload(Upload{ID: NewID(), Module: "photos", Name: "sample.bin", Files: []UploadFile{{Name: "sample.bin", Size: int64(len(data))}}})
	if err != nil {
		t.Fatal(err)
	}
	tr := Transfer{ID: NewID(), Parent: up.ID, Module: up.Module, Name: "sample.bin", Size: int64(len(data)), Hashes: []string{}}
	for start := int64(0); start < int64(len(data)); start += ChunkSize {
		tr.Hashes = append(tr.Hashes, hash(data[start:min(start+ChunkSize, int64(len(data)))]))
	}
	return tr
}
func TestChunkResumeRestartConflictAndPinnedSnapshot(t *testing.T) {
	ctx := context.Background()
	l := testLibrary(t)
	data := bytes.Repeat([]byte("abc-resume"), int(ChunkSize/10)+30)
	tr := transferFixture(t, l, data)
	if _, err := l.CreateTransfer(ctx, tr, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := l.ReceiveChunk(ctx, tr.ID, 0, bytes.NewReader(data[:ChunkSize]), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := l.FinishTransfer(ctx, tr.ID, nil, media.Converter{}); err == nil {
		t.Fatal("partial upload committed")
	}
	changed := tr
	changed.Hashes = append([]string{}, tr.Hashes...)
	changed.Hashes[0] = hash([]byte("different"))
	if _, err := l.CreateTransfer(ctx, changed, nil); err == nil {
		t.Fatal("mixed sources accepted")
	}
	root := l.Root
	l.Close()
	var err error
	l, err = Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	tasks, err := l.Transfers(nil)
	if err != nil || len(tasks) != 1 || len(tasks[0].Done) != 1 {
		t.Fatal("lost resume", tasks, err)
	}
	pin := filepath.Join(t.TempDir(), "snapshot")
	if err = l.Snapshot(ctx, pin); err != nil {
		t.Fatal(err)
	}
	if _, err = l.ReceiveChunk(ctx, tr.ID, 1, bytes.NewReader([]byte("bad")), nil); err == nil {
		t.Fatal("bad chunk accepted")
	}
	if _, err = l.ReceiveChunk(ctx, tr.ID, 1, bytes.NewReader(data[ChunkSize:]), nil); err != nil {
		t.Fatal(err)
	}
	if _, err = l.FinishTransfer(ctx, tr.ID, nil, media.Converter{}); err != nil {
		t.Fatal(err)
	}
	if _, err = l.FinishTransfer(ctx, tr.ID, nil, media.Converter{}); err != nil {
		t.Fatal("lost-response retry", err)
	}
	item, err := l.Finish(tr.Parent)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(l.ObjectPath(item.Pages[0].Object))
	if err != nil || !bytes.Equal(got, data) {
		t.Fatal("bytes changed", err)
	}
	if err = l.Collect(); err != nil {
		t.Fatal(err)
	}
	pinned, err := Open(pin)
	if err != nil {
		t.Fatal(err)
	}
	defer pinned.Close()
	if err = pinned.Verify(ctx); err != nil {
		t.Fatal("GC damaged pinned chunks", err)
	}
	if err = l.Verify(ctx); err != nil {
		t.Fatal(err)
	}
}
func TestPrivateChunksEncryptNamesContentAndResume(t *testing.T) {
	l := testLibrary(t)
	a := privateAccess(t)
	ctx := context.Background()
	data := []byte("synthetic-secret-chunk-body-f985ac")
	tr := Transfer{ID: NewID(), Module: "files", Name: "synthetic-secret-file-f8cc.txt", Size: int64(len(data)), Hashes: []string{hash(data)}}
	if _, err := l.CreateTransfer(ctx, tr, a); err != nil {
		t.Fatal(err)
	}
	if _, err := l.ReceiveChunk(ctx, tr.ID, 0, bytes.NewReader(data), a); err != nil {
		t.Fatal(err)
	}
	public, err := l.Transfers(nil)
	if err != nil || len(public) != 0 {
		t.Fatal("private tasks leaked")
	}
	if _, err = l.transfer(tr.ID, nil); !errors.Is(err, ErrMissing) {
		t.Fatal("cross namespace", err)
	}
	err = filepath.WalkDir(l.Root, func(path string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if d.IsDir() {
			return nil
		}
		b, e := os.ReadFile(path)
		if e != nil {
			return e
		}
		for _, secret := range [][]byte{data, []byte(tr.Name), []byte(tr.Hashes[0])} {
			if bytes.Contains(b, secret) {
				t.Fatalf("plaintext on disk: %s", path)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	root := l.Root
	l.Close()
	l, err = Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	result, err := l.FinishTransfer(ctx, tr.ID, a, media.Converter{})
	if err != nil {
		t.Fatal(err)
	}
	it := result.(PrivateItem)
	it, err = l.PrivateItem(a, it.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(privateBytes(t, l, a, it, false), data) {
		t.Fatal("private original changed")
	}
	a.Lock()
	if _, err = l.Transfers(a); err == nil {
		t.Fatal("locked task names exposed")
	}
}
func TestChunkTamperAndCancellation(t *testing.T) {
	l := testLibrary(t)
	ctx := context.Background()
	data := []byte("complete sample")
	tr := transferFixture(t, l, data)
	l.CreateTransfer(ctx, tr, nil)
	cancelCtx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := l.ReceiveChunk(cancelCtx, tr.ID, 0, bytes.NewReader(data), nil); err == nil {
		t.Fatal("cancelled write succeeded")
	}
	l.ReceiveChunk(ctx, tr.ID, 0, bytes.NewReader(data), nil)
	var id string
	l.db.QueryRow("SELECT object FROM transfer_chunks WHERE transfer_id=?", tr.ID).Scan(&id)
	os.WriteFile(filepath.Join(l.Root, "chunks", id), []byte("tampered"), 0600)
	if _, err := l.FinishTransfer(ctx, tr.ID, nil, media.Converter{}); err == nil {
		t.Fatal("tamper accepted")
	}
	if err := l.Verify(ctx); err == nil {
		t.Fatal("verification missed tamper")
	}
	if err := l.Cancel(tr.Parent); err != nil {
		t.Fatal(err)
	}
	if err := l.Collect(); err != nil {
		t.Fatal(err)
	}
	if err := l.Verify(ctx); err != nil {
		t.Fatal("cancelled refs retained", err)
	}
}

type blockingReader struct {
	started chan struct{}
	release chan struct{}
	reader  io.Reader
	first   bool
}

func (r *blockingReader) Read(p []byte) (int, error) {
	if !r.first {
		r.first = true
		close(r.started)
		<-r.release
	}
	return r.reader.Read(p)
}
func TestGCWaitsForPrivatePublish(t *testing.T) {
	l := testLibrary(t)
	a := privateAccess(t)
	r := &blockingReader{started: make(chan struct{}), release: make(chan struct{}), reader: bytes.NewReader([]byte("content"))}
	done := make(chan error, 1)
	go func() { _, e := l.ReceivePrivate(context.Background(), a, NewID(), "files", "file", 7, r); done <- e }()
	<-r.started
	collected := make(chan error, 1)
	go func() { collected <- l.Collect() }()
	close(r.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := <-collected; err != nil {
		t.Fatal(err)
	}
	if err := l.Verify(context.Background()); err != nil {
		t.Fatal(err)
	}
}
