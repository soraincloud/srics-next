package library

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/soraincloud/srics-next/internal/vault"
)

func privateAccess(t *testing.T) *vault.Access {
	t.Helper()
	key, e := age.GenerateX25519Identity()
	if e != nil {
		t.Fatal(e)
	}
	a := vault.NewAccess(context.Background(), key, time.Hour)
	t.Cleanup(a.Lock)
	return a
}
func privateUpload(t *testing.T, l *Library, a *vault.Access, module, name string, data []byte) PrivateItem {
	t.Helper()
	it, e := l.ReceivePrivate(context.Background(), a, NewID(), module, name, int64(len(data)), bytes.NewReader(data))
	if e != nil {
		t.Fatal(e)
	}
	it, e = l.PrivateItem(a, it.ID)
	if e != nil {
		t.Fatal(e)
	}
	return it
}
func privateBytes(t *testing.T, l *Library, a *vault.Access, it PrivateItem, thumb bool) []byte {
	t.Helper()
	_, ctx, _, _ := a.Snapshot()
	r, _, e := l.PrivateRead(ctx, a, it, thumb)
	if e != nil {
		t.Fatal(e)
	}
	defer r.Close()
	b, e := io.ReadAll(r)
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func TestPrivateStoragePrivacyRetryTrashAndBinding(t *testing.T) {
	l := testLibrary(t)
	a := privateAccess(t)
	ctx := context.Background()
	original := []byte(strings.Repeat("SYNTHETIC-PRIVATE-CONTENT-390ab", 6000))
	name := "SYNTHETIC-PRIVATE-NAME-e74.txt"
	it := privateUpload(t, l, a, "files", name, original)
	if !bytes.Equal(privateBytes(t, l, a, it, false), original) {
		t.Fatal("original changed")
	}
	retry, e := l.ReceivePrivate(ctx, a, it.ID, it.Module, name, it.Size, bytes.NewReader(original))
	if e != nil || retry.Object != it.Object {
		t.Fatal("retry duplicated", e)
	}
	if _, e = l.ReceivePrivate(ctx, a, it.ID, it.Module, "different", it.Size, bytes.NewReader(original)); !errors.Is(e, ErrConflict) {
		t.Fatal("changed retry", e)
	}
	renamed, e := l.ChangePrivate(ctx, a, it.ID, "SYNTHETIC-DISPLAY-88", "rename", it.Revision)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = l.ChangePrivate(ctx, a, it.ID, "stale", "rename", it.Revision); !errors.Is(e, ErrConflict) {
		t.Fatal("stale overwrite", e)
	}
	removed, e := l.ChangePrivate(ctx, a, it.ID, "", "trash", renamed.Revision)
	if e != nil || removed.Deleted == "" {
		t.Fatal(e)
	}
	restored, e := l.ChangePrivate(ctx, a, it.ID, "", "restore", removed.Revision)
	if e != nil || restored.Deleted != "" || restored.Name != renamed.Name {
		t.Fatal(e)
	}
	photo := privateUpload(t, l, a, "private", "SYNTHETIC-PRIVATE-PHOTO.png", fixture(t))
	if !bytes.Equal(privateBytes(t, l, a, photo, false), fixture(t)) || len(privateBytes(t, l, a, photo, true)) == 0 {
		t.Fatal("photo original/preview")
	}
	ordinary, e := l.Items("all", false)
	if e != nil || len(ordinary) != 0 {
		t.Fatal("private content leaked to ordinary library")
	}
	_ = filepath.Walk(l.Root, func(path string, info os.FileInfo, e error) error {
		if e != nil {
			t.Fatal(e)
		}
		if info.IsDir() {
			return nil
		}
		b, e := os.ReadFile(path)
		if e != nil {
			t.Fatal(e)
		}
		for _, needle := range [][]byte{[]byte(name), []byte(renamed.Name), []byte("SYNTHETIC-PRIVATE-CONTENT-390ab"), []byte(photo.Original), []byte(it.SHA256)} {
			if bytes.Contains(b, needle) {
				t.Fatalf("plaintext persisted in %s", path)
			}
		}
		return nil
	})
	if err := l.Verify(ctx); err != nil {
		t.Fatal(err)
	}
	row, _ := l.privateRow(it.ID)
	_, e = l.db.Exec("UPDATE private_items SET payload=? WHERE id=?", row.payload, photo.ID)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = l.PrivateItem(a, photo.ID); e == nil {
		t.Fatal("metadata swapping allowed")
	}
	a.Lock()
	if _, e = l.PrivateItem(a, it.ID); !errors.Is(e, vault.ErrLocked) {
		t.Fatal("locked key read", e)
	}
}
func TestPrivateRejectsCorruptionAndCancelledUpload(t *testing.T) {
	l := testLibrary(t)
	a := privateAccess(t)
	ctx := context.Background()
	it := privateUpload(t, l, a, "files", "secret.txt", bytes.Repeat([]byte("private"), 24000))
	encrypted, e := os.ReadFile(l.PrivatePath(it.Object))
	if e != nil {
		t.Fatal(e)
	}
	for _, bad := range [][]byte{encrypted[:len(encrypted)-1], append(append([]byte{}, encrypted[:len(encrypted)-1]...), encrypted[len(encrypted)-1]^1)} {
		if e = os.WriteFile(l.PrivatePath(it.Object), bad, 0600); e != nil {
			t.Fatal(e)
		}
		if r, _, e := l.PrivateRead(ctx, a, it, false); e == nil || r != nil {
			t.Fatal("corrupt data exposed")
		}
		if e = l.Verify(ctx); e == nil {
			t.Fatal("locked ciphertext validation missed corruption")
		}
	}
	id := NewID()
	src := &lockOnRead{a: a}
	if _, e = l.ReceivePrivate(ctx, a, id, "files", "must-not-commit", 1, src); e == nil {
		t.Fatal("lock racing commit accepted")
	}
	if _, e = l.privateRow(id); !errors.Is(e, ErrMissing) {
		t.Fatal("locked upload published")
	}
	paths, _ := filepath.Glob(filepath.Join(l.Root, "private-objects", ".pending-*"))
	if len(paths) > 0 {
		t.Fatal("abandoned encrypted tempfiles")
	}
}

type lockOnRead struct {
	a    *vault.Access
	done bool
}

func (r *lockOnRead) Read(p []byte) (int, error) {
	if r.done {
		return 0, io.EOF
	}
	r.done = true
	r.a.Lock()
	p[0] = 42
	return 1, nil
}
func TestPrivateSnapshotCanRestoreWrappedKeyWhileLocked(t *testing.T) {
	l := testLibrary(t)
	wrapped, e := l.PrepareVault("independent-synthetic-vault", " ")
	if e != nil {
		t.Fatal(e)
	}
	if e = l.SetSetting("vault-key", wrapped); e != nil {
		t.Fatal(e)
	}
	key, e := vault.Unlock(wrapped, "independent-synthetic-vault")
	if e != nil {
		t.Fatal(e)
	}
	a := vault.NewAccess(context.Background(), key, time.Hour)
	data := []byte("synthetic restore secret")
	it := privateUpload(t, l, a, "files", "recover.txt", data)
	a.Lock()
	dest := filepath.Join(t.TempDir(), "restored")
	if e = l.Snapshot(context.Background(), dest); e != nil {
		t.Fatal(e)
	}
	restored, e := Open(dest)
	if e != nil {
		t.Fatal(e)
	}
	defer restored.Close()
	if e = restored.Verify(context.Background()); e != nil {
		t.Fatal(e)
	}
	saved, _ := restored.Setting("vault-key")
	if _, e = vault.Unlock(saved, "wrong-passphrase"); e == nil {
		t.Fatal("wrong key accepted")
	}
	key, e = vault.Unlock(saved, "independent-synthetic-vault")
	if e != nil {
		t.Fatal(e)
	}
	unlocked := vault.NewAccess(context.Background(), key, time.Hour)
	defer unlocked.Lock()
	it, e = restored.PrivateItem(unlocked, it.ID)
	if e != nil {
		t.Fatal(e)
	}
	if !bytes.Equal(privateBytes(t, restored, unlocked, it, false), data) {
		t.Fatal("restore changed bytes")
	}
}

func TestPrivateReadStopsAfterSessionLock(t *testing.T) {
	l := testLibrary(t)
	a := privateAccess(t)
	it := privateUpload(t, l, a, "files", "in-flight.txt", bytes.Repeat([]byte("private streaming bytes"), 10000))
	_, ctx, _, err := a.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	reader, _, err := l.PrivateRead(ctx, a, it, false)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	buffer := make([]byte, 1024)
	if n, err := reader.Read(buffer); n == 0 || err != nil {
		t.Fatal(err)
	}
	a.Lock()
	if n, err := reader.Read(buffer); n != 0 || err == nil {
		t.Fatal("read continued after lock", n, err)
	}
}
