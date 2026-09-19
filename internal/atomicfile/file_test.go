package atomicfile

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

func TestPublishNeverOverwrites(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "object")
	var success atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if WriteNew(path, func(w io.Writer) error { _, err := w.Write([]byte("complete")); return err }) == nil {
				success.Add(1)
			}
		}()
	}
	wg.Wait()
	if success.Load() != 1 {
		t.Fatalf("%d writers published", success.Load())
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "complete" {
		t.Fatalf("incomplete publish: %q %v", data, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatal("temporary files left behind", err)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatalf("permissions %v", info.Mode())
	}
}
func TestFailedWriteIsNotPublished(t *testing.T) {
	path := filepath.Join(t.TempDir(), "object")
	err := WriteNew(path, func(w io.Writer) error { w.Write([]byte("partial")); return errors.New("simulated source failure") })
	if err == nil {
		t.Fatal("expected failure")
	}
	if _, err = os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("partial object published")
	}
}
