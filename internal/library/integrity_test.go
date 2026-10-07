package library

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConflictingReferencesCannotPassVerificationOrCleanup(t *testing.T) {
	l := testLibrary(t)
	it := upload(t, l, "photos", "original.png", fixture(t))
	orphan, err := l.put([]byte("retain while metadata is suspect"))
	if err != nil {
		t.Fatal(err)
	}
	it.Pages[0].SHA256 = strings.Repeat("0", 64)
	b, _ := json.Marshal(it.Pages)
	if _, err = l.db.Exec("UPDATE items SET pages=? WHERE id=?", b, it.ID); err != nil {
		t.Fatal(err)
	}
	// The completed upload still references the same object with its real hash.
	// Deduplicating by object ID must not silently hide the damaged item record.
	if err = l.Verify(context.Background()); err == nil {
		t.Error("conflicting hashes passed verification")
	}
	if err = l.Collect(); err == nil {
		t.Error("conflicting hashes allowed cleanup")
	}
	if _, err = os.Stat(l.ObjectPath(orphan.Object)); err != nil {
		t.Error("suspect index deleted an object", err)
	}
}

func TestOriginalRejectsSameSizeCorruptionAndPinsOpenFile(t *testing.T) {
	l := testLibrary(t)
	data := fixture(t)
	it := upload(t, l, "photos", "original.png", data)
	p := it.Pages[0]
	ctx := context.Background()
	f, err := l.OpenOriginal(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := os.Rename(l.ObjectPath(p.Object), l.ObjectPath(NewID())); err != nil {
		t.Fatal(err)
	}
	bad := bytes.Clone(data)
	bad[len(bad)-1] ^= 1
	if err := os.WriteFile(l.ObjectPath(p.Object), bad, 0600); err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(f)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatal("open descriptor did not preserve validated original", err)
	}
	if f, err := l.OpenOriginal(ctx, p); err == nil {
		f.Close()
		t.Fatal("same-size corruption returned as original")
	}
	var out bytes.Buffer
	if err := l.CopyOriginal(ctx, p, &out); err == nil {
		t.Fatal("corrupt export reported success")
	}
	if err := l.Verify(ctx); err == nil {
		t.Fatal("backup verification accepted corrupt original")
	}
	if err := os.WriteFile(l.ObjectPath(p.Object), data, 0600); err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if f, err := l.OpenOriginal(cancelled, p); err == nil {
		f.Close()
		t.Fatal("cancelled read succeeded")
	}
}

func TestInvalidMetadataStopsGarbageCollectionBeforeAnyDeletion(t *testing.T) {
	l := testLibrary(t)
	it := upload(t, l, "photos", "original.png", fixture(t))
	// Also create an actual orphan: no sweep is permitted once the index is suspect.
	orphan, err := l.put([]byte("preserve until the index is repaired"))
	if err != nil {
		t.Fatal(err)
	}
	for _, pages := range []string{`[]`, `[{}]`, `null`} {
		if _, err := l.db.Exec("UPDATE items SET pages=? WHERE id=?", pages, it.ID); err != nil {
			t.Fatal(err)
		}
		if err := l.Collect(); err == nil {
			t.Fatal("invalid index allowed collection", pages)
		}
		if err := l.Verify(context.Background()); err == nil {
			t.Fatal("invalid index allowed backup", pages)
		}
		for _, id := range []string{it.Pages[0].Object, orphan.Object} {
			if _, err := os.Stat(l.ObjectPath(id)); err != nil {
				t.Fatal("file lost during rejected collection", err)
			}
		}
	}
	pages, _ := json.Marshal(it.Pages)
	if _, err := l.db.Exec("UPDATE items SET pages=? WHERE id=?", string(pages), it.ID); err != nil {
		t.Fatal(err)
	}
	if err := l.Collect(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(l.ObjectPath(orphan.Object)); !os.IsNotExist(err) {
		t.Fatal("valid index did not permit orphan cleanup", err)
	}
}

func TestDatabaseDriveCacheFlushSurvivesConnectionReplacement(t *testing.T) {
	l := testLibrary(t)
	l.db.SetMaxIdleConns(0)
	for _, name := range []string{"fullfsync", "checkpoint_fullfsync"} {
		var on int
		if err := l.db.QueryRow("PRAGMA " + name).Scan(&on); err != nil || on != 1 {
			t.Fatal(name, on, err)
		}
	}
}

func TestTruncatedDatabaseNeverBecomesEmptyLibrary(t *testing.T) {
	l := testLibrary(t)
	data := fixture(t)
	it := upload(t, l, "photos", "original.png", data)
	root, original := l.Root, l.ObjectPath(it.Pages[0].Object)
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	index := filepath.Join(root, "index.db")
	if err := os.Truncate(index, 0); err != nil {
		t.Fatal(err)
	}
	if reopened, err := Open(root); err == nil {
		defer reopened.Close()
		// This is the destructive consequence of accepting the empty index.
		if err := reopened.Collect(); err != nil {
			t.Fatal(err)
		}
		_, statErr := os.Stat(original)
		t.Fatalf("truncated database accepted as new library; original after GC: %v", statErr)
	}
	if info, err := os.Stat(index); err != nil || info.Size() != 0 {
		t.Fatal("damaged index was rewritten", err)
	}
	if got, err := os.ReadFile(original); err != nil || !bytes.Equal(got, data) {
		t.Fatal("failed startup changed original", err)
	}
}

func TestDamagedSchemaStopsStartupAndPreservesObjects(t *testing.T) {
	for _, damage := range []string{"PRAGMA user_version=0", "DROP TABLE chapter_versions", "DROP TABLE novel_state", "ALTER TABLE items DROP COLUMN pages"} {
		t.Run(damage, func(t *testing.T) {
			l := testLibrary(t)
			data := fixture(t)
			it := upload(t, l, "photos", "original.png", data)
			root, original := l.Root, l.ObjectPath(it.Pages[0].Object)
			if _, err := l.db.Exec(damage); err != nil {
				t.Fatal(err)
			}
			if err := l.Close(); err != nil {
				t.Fatal(err)
			}
			if reopened, err := Open(root); err == nil {
				reopened.Close()
				t.Fatal("damaged schema accepted")
			}
			if got, err := os.ReadFile(original); err != nil || !bytes.Equal(got, data) {
				t.Fatal("failed startup changed original", err)
			}
		})
	}
}
