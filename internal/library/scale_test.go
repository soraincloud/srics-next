//go:build scale

package library

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/soraincloud/srics-next/internal/media"
	"io"
	"runtime"
	"testing"
	"time"
)

func TestScalePrivate512MiBResumeAndRead(t *testing.T) {
	ctx := context.Background()
	l := testLibrary(t)
	a := privateAccess(t)
	chunk := bytes.Repeat([]byte("0123456789abcdef"), int(ChunkSize)/16)
	tr := Transfer{ID: NewID(), Module: "files", Name: "synthetic-512MiB.bin", Size: 64 * ChunkSize, Hashes: make([]string, 64)}
	for i := range tr.Hashes {
		tr.Hashes[i] = hash(chunk)
	}
	start := time.Now()
	if _, err := l.CreateTransfer(ctx, tr, a); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 16; i++ {
		if _, err := l.ReceiveChunk(ctx, tr.ID, i, bytes.NewReader(chunk), a); err != nil {
			t.Fatal(err)
		}
	}
	root := l.Root
	l.Close()
	var err error
	l, err = Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	resumed, err := l.CreateTransfer(ctx, tr, a)
	if err != nil || len(resumed.Done) != 16 {
		t.Fatal(resumed, err)
	}
	for i := 16; i < 64; i++ {
		if _, err = l.ReceiveChunk(ctx, tr.ID, i, bytes.NewReader(chunk), a); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = l.FinishTransfer(ctx, tr.ID, a, media.Converter{}); err != nil {
		t.Fatal(err)
	}
	it, err := l.PrivateItem(a, tr.ID)
	if err != nil {
		t.Fatal(err)
	}
	r, size, err := l.PrivateRead(ctx, a, it, false)
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.New()
	n, err := io.Copy(h, r)
	r.Close()
	if err != nil || n != tr.Size || size != tr.Size || hex.EncodeToString(h.Sum(nil)) != it.SHA256 {
		t.Fatal(n, size, err)
	}
	if err = l.Collect(); err != nil {
		t.Fatal(err)
	}
	if err = l.Verify(ctx); err != nil {
		t.Fatal(err)
	}
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	t.Logf("512 MiB encrypted upload + restart at 128 MiB + plaintext hash download + GC + verification: %s; Go heap reserved %.1f MiB", time.Since(start), float64(m.HeapSys)/(1<<20))
}
func TestScale10000Entries(t *testing.T) {
	l := testLibrary(t)
	p, err := l.put([]byte("synthetic metadata scale fixture"))
	if err != nil {
		t.Fatal(err)
	}
	p.Name = "sample.bin"
	pages, _ := json.Marshal([]Page{p})
	tx, err := l.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10000; i++ {
		_, err = tx.Exec("INSERT INTO items(id,module,name,tags,created,deleted,revision,pages,progress) VALUES(?,?,?,?,?,'',1,?,0)", NewID(), "photos", fmt.Sprintf("synthetic-%05d", i), "[]", time.Now().UTC().Format(time.RFC3339Nano), string(pages))
		if err != nil {
			t.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	items, err := l.Items("photos", false)
	if err != nil || len(items) != 10000 {
		t.Fatal(len(items), err)
	}
	t.Logf("10000 ordinary entries loaded: %s", time.Since(start))
	start = time.Now()
	if err = l.Collect(); err != nil {
		t.Fatal(err)
	}
	if err = l.Verify(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Logf("10000 references collected and verified: %s (shared synthetic object, not 100 GB physical storage)", time.Since(start))
}
