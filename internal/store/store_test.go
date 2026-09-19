package store

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"filippo.io/age"
	"github.com/soraincloud/srics-next/internal/vault"
)

func TestPrivateObjectsAndSnapshot(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	key, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	db, err := Create(filepath.Join(root, "live"), []byte("test wrapped-key placeholder"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	a, err := db.Put(ctx, "sensitive-name", bytes.NewBufferString("sensitive-content"), key, true)
	if err != nil {
		t.Fatal(err)
	}
	b, err := db.Put(ctx, "sensitive-name", bytes.NewBufferString("second-content"), key, true)
	if err != nil || a == b {
		t.Fatal("same name overwrote data", err)
	}
	if _, _, err = db.ReadFixture(ctx, a, nil); !errors.Is(err, vault.ErrLocked) {
		t.Fatal("locked object readable")
	}
	objects, err := db.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, obj := range objects {
		if bytes.Contains(obj.Metadata, []byte("sensitive")) {
			t.Fatal("metadata plaintext")
		}
	}
	snapshot := filepath.Join(root, "snapshot")
	if err = db.Snapshot(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Put(ctx, "later", bytes.NewBufferString("later"), nil, false); err != nil {
		t.Fatal(err)
	}
	snap, err := Open(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	defer snap.Close()
	objects, err = snap.List(ctx)
	if err != nil || len(objects) != 2 {
		t.Fatal("inconsistent snapshot", err)
	}
	_, got, err := snap.ReadFixture(ctx, a, key)
	if err != nil || string(got) != "sensitive-content" {
		t.Fatal("snapshot content", err)
	}
	// Swapping authenticated metadata must still fail its object-ID binding.
	if _, err = db.db.Exec("UPDATE objects SET metadata=(SELECT metadata FROM objects WHERE id=?) WHERE id=?", b, a); err != nil {
		t.Fatal(err)
	}
	if _, _, err = db.ReadFixture(ctx, a, key); err == nil {
		t.Fatal("metadata swap accepted")
	}
}
func TestOpenMissingNeverCreatesData(t *testing.T) {
	root := filepath.Join(t.TempDir(), "missing")
	if _, err := Open(root); err == nil {
		t.Fatal("missing store opened")
	}
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("missing store was created")
	}
}
func TestSnapshotCannotOverwrite(t *testing.T) {
	root := t.TempDir()
	db, err := Create(filepath.Join(root, "live"), []byte("fixture"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	dest := filepath.Join(root, "existing")
	os.Mkdir(dest, 0700)
	sentinel := filepath.Join(dest, "keep")
	os.WriteFile(sentinel, []byte("keep"), 0600)
	if err = db.Snapshot(context.Background(), dest); err == nil {
		t.Fatal("existing target accepted")
	}
	if data, _ := os.ReadFile(sentinel); string(data) != "keep" {
		t.Fatal("existing data changed")
	}
}
