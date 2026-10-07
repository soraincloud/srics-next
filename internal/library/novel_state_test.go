package library

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"os"
	"path/filepath"
	"testing"
)

func TestNovelStatusAndBookmarkSurviveReopenAndSnapshot(t *testing.T) {
	l := testLibrary(t)
	n, c := novelFixture(t, l)
	it, _ := l.Item(n.ID)
	if it.Completed {
		t.Fatal("new novel must be unfinished")
	}
	completed, err := l.SetNovelCompleted(n.ID, true, it.Revision)
	if err != nil || !completed.Completed {
		t.Fatal(completed, err)
	}
	if _, err = l.SetNovelCompleted(n.ID, true, it.Revision); err != nil {
		t.Fatal("lost response retry", err)
	}
	if _, err = l.SetNovelCompleted(n.ID, false, it.Revision); !errors.Is(err, ErrConflict) {
		t.Fatal("stale status overwrote newer state", err)
	}
	bookmark := NovelBookmark{Chapter: c.ID, Paragraph: 1, Fraction: .375, Revision: c.Revision}
	if err = l.SaveNovelBookmark(n.ID, bookmark); err != nil {
		t.Fatal(err)
	}
	items, _ := l.Items("novels", false)
	if len(items) != 1 || !items[0].Completed {
		t.Fatal("list omitted completed status")
	}
	if err = l.Update(n.ID, "改名后", []string{"分类"}, completed.Revision); err != nil {
		t.Fatal(err)
	}
	check := func(store *Library) {
		t.Helper()
		got, e := store.Novel(n.ID)
		if e != nil || !got.Item.Completed || got.Item.Name != "改名后" || got.Reading != c.ID || got.Bookmark == nil || got.Bookmark.Paragraph != 1 || got.Bookmark.Fraction != .375 || got.Bookmark.Revision != c.Revision || got.Bookmark.Updated == "" {
			t.Fatal("missing persistent reading/status", got, e)
		}
		if e = store.Verify(context.Background()); e != nil {
			t.Fatal(e)
		}
	}
	check(l)
	snapshot := filepath.Join(t.TempDir(), "snapshot")
	if err = l.Snapshot(context.Background(), snapshot); err != nil {
		t.Fatal(err)
	}
	root := l.Root
	if err = l.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	check(reopened)
	item, _ := reopened.Item(n.ID)
	if _, err = reopened.SetNovelCompleted(n.ID, false, item.Revision); err != nil {
		t.Fatal(err)
	}
	pinned, err := Open(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	defer pinned.Close()
	check(pinned)
	if err = reopened.TrashChapter(n.ID, c.ID, c.Revision, false); err != nil {
		t.Fatal(err)
	}
	got, _ := reopened.Novel(n.ID)
	if got.Bookmark != nil || got.Reading != "" {
		t.Fatal("deleted chapter retained bookmark", got)
	}
	if err = reopened.Trash(n.ID, false); err != nil {
		t.Fatal(err)
	}
	if err = reopened.Purge(n.ID); err != nil {
		t.Fatal("novel state blocked purge", err)
	}
	if err = reopened.Verify(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestNovelBookmarkRejectsInvalidAndIsolatedPositions(t *testing.T) {
	l := testLibrary(t)
	n, c := novelFixture(t, l)
	other, _ := l.CreateNovel(NewID(), "另一本", nil)
	if err := l.SaveNovelBookmark(other.ID, NovelBookmark{Chapter: c.ID}); !errors.Is(err, ErrMissing) {
		t.Fatal("cross-novel bookmark accepted", err)
	}
	for _, b := range []NovelBookmark{
		{Chapter: c.ID, Paragraph: -1}, {Chapter: c.ID, Paragraph: 2},
		{Chapter: c.ID, Fraction: -0.1}, {Chapter: c.ID, Fraction: 1.01},
		{Chapter: c.ID, Fraction: math.NaN()}, {Chapter: c.ID, Fraction: math.Inf(1)},
		{Chapter: c.ID, Revision: -1},
	} {
		if err := l.SaveNovelBookmark(n.ID, b); err == nil {
			t.Fatal("invalid bookmark accepted", b)
		}
	}
	if err := l.SaveNovelBookmark(n.ID, NovelBookmark{Chapter: c.ID, Revision: c.Revision + 1}); !errors.Is(err, ErrConflict) {
		t.Fatal("stale body accepted", err)
	}
	got, _ := l.Novel(n.ID)
	if got.Bookmark != nil {
		t.Fatal("rejected request mutated bookmark")
	}
	ordinary := upload(t, l, "images", "sample.png", fixture(t))
	if _, err := l.SetNovelCompleted(ordinary.ID, true, ordinary.Revision); !errors.Is(err, ErrMissing) {
		t.Fatal("ordinary item accepted novel status", err)
	}
	l.FreeSpace = func(string) (uint64, error) { return 1, nil }
	if err := l.SaveNovelBookmark(n.ID, NovelBookmark{Chapter: c.ID}); err == nil {
		t.Fatal("space guard omitted", err)
	}
	it, _ := l.Item(n.ID)
	if _, err := l.SetNovelCompleted(n.ID, true, it.Revision); err == nil {
		t.Fatal("status space guard omitted", err)
	}
}

func TestNovelStateMigrationPreservesLegacyChapterAndIndex(t *testing.T) {
	l := testLibrary(t)
	n, c := novelFixture(t, l)
	if err := l.NovelProgress(n.ID, c.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := l.db.Exec("DROP TABLE novel_state; PRAGMA user_version=6"); err != nil {
		t.Fatal(err)
	}
	root := l.Root
	l.Close()
	migrated, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()
	got, err := migrated.Novel(n.ID)
	if err != nil || got.Item.Completed || got.Bookmark == nil || got.Bookmark.Chapter != c.ID || got.Bookmark.Paragraph != 0 {
		t.Fatal("legacy reading lost", got, err)
	}
	paths, _ := filepath.Glob(filepath.Join(root, "staging", "before-novel-state-*.db"))
	if len(paths) != 1 {
		t.Fatal("missing pre-upgrade snapshot")
	}
	info, _ := os.Stat(paths[0])
	if info.Mode().Perm() != 0600 {
		t.Fatal("unsafe migration copy")
	}
	db, err := sql.Open("sqlite3", paths[0]+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var version int
	var chapter string
	if err = db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 6 {
		t.Fatal(version, err)
	}
	if err = db.QueryRow("SELECT chapter_id FROM novel_reading WHERE novel_id=?", n.ID).Scan(&chapter); err != nil || chapter != c.ID {
		t.Fatal("original reading missing", err)
	}
}
