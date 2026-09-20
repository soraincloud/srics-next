package library

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPurgePreservesPinnedBackupAndLiveObjects(t *testing.T) {
	l := testLibrary(t)
	ctx := context.Background()
	old := upload(t, l, "images", "old.png", fixture(t))
	live := upload(t, l, "images", "keep.png", fixture(t))
	if err := l.Purge(live.ID); err == nil {
		t.Fatal("live purge accepted")
	}
	l.Trash(old.ID, false)
	snapshot := filepath.Join(t.TempDir(), "pinned")
	if err := l.Snapshot(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	if err := l.Purge(old.ID); err != nil {
		t.Fatal(err)
	}
	if _, e := l.Item(old.ID); !errors.Is(e, ErrMissing) {
		t.Fatal(e)
	}
	if _, e := os.Stat(l.ObjectPath(old.Pages[0].Object)); !os.IsNotExist(e) {
		t.Fatal("purged bytes remain", e)
	}
	pin, e := Open(snapshot)
	if e != nil {
		t.Fatal(e)
	}
	defer pin.Close()
	if e = pin.Verify(ctx); e != nil {
		t.Fatal(e)
	}
	if e = l.Verify(ctx); e != nil {
		t.Fatal(e)
	}
}
func TestTrashExpiryPrivateUnlockAndNovelRelations(t *testing.T) {
	l := testLibrary(t)
	ctx := context.Background()
	n, c := novelFixture(t, l)
	if err := l.TrashChapter(n.ID, c.ID, c.Revision, false); err != nil {
		t.Fatal(err)
	}
	old := time.Now().UTC().AddDate(0, 0, -31).Format(time.RFC3339Nano)
	l.db.Exec("UPDATE chapters SET deleted=? WHERE id=?", old, c.ID)
	if err := l.ExpireTrash(ctx, 30); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Chapter(n.ID, c.ID); !errors.Is(err, ErrMissing) {
		t.Fatal("expired chapter", err)
	}
	if err := l.Trash(n.ID, false); err != nil {
		t.Fatal(err)
	}
	if err := l.Purge(n.ID); err != nil {
		t.Fatal(err)
	}
	a := privateAccess(t)
	it := privateUpload(t, l, a, "files", "secret", []byte("body"))
	it, e := l.ChangePrivate(ctx, a, it.ID, "", "trash", it.Revision)
	if e != nil {
		t.Fatal(e)
	}
	r, e := l.privateRow(it.ID)
	if e != nil {
		t.Fatal(e)
	}
	key, _, _, _ := a.Snapshot()
	it.Deleted = old
	r.payload, e = encodePrivate(it, r, key)
	if e != nil {
		t.Fatal(e)
	}
	l.db.Exec("UPDATE private_items SET payload=? WHERE id=?", r.payload, it.ID)
	if e = l.ExpirePrivateTrash(ctx, a, 30); e != nil {
		t.Fatal(e)
	}
	if _, e = l.PrivateItem(a, it.ID); !errors.Is(e, ErrMissing) {
		t.Fatal("private not purged", e)
	}
	if e = l.Verify(ctx); e != nil {
		t.Fatal(e)
	}
}
