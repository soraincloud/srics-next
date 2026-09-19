package library

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func novelFixture(t *testing.T, l *Library) (Item, Chapter) {
	t.Helper()
	n, err := l.CreateNovel(NewID(), "测试小说", []string{"长篇", "示例"})
	if err != nil {
		t.Fatal(err)
	}
	c, err := l.CreateChapter(n.ID, NewID(), "第一章", "初稿\n第二段", n.Revision)
	if err != nil {
		t.Fatal(err)
	}
	return n, c
}
func TestNovelConflictsHistoryAndRetry(t *testing.T) {
	l := testLibrary(t)
	n, c := novelFixture(t, l)
	saved, err := l.SaveChapter(n.ID, c.ID, c.Title, "设备甲的新正文", c.Revision, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = l.SaveChapter(n.ID, c.ID, c.Title, "设备乙的旧正文", c.Revision, false); !errors.Is(err, ErrConflict) {
		t.Fatal("stale edit overwrote current", err)
	}
	retry, err := l.SaveChapter(n.ID, c.ID, c.Title, "设备甲的新正文", c.Revision, false)
	if err != nil || retry.Revision != saved.Revision {
		t.Fatal("lost-response retry duplicated revision", err)
	}
	versions, err := l.Versions(n.ID, c.ID)
	if err != nil || len(versions) != 1 {
		t.Fatal(versions, err)
	}
	old, err := l.Version(n.ID, c.ID, versions[0].Revision)
	if err != nil || old.Body != c.Body {
		t.Fatal("original content not retained", old, err)
	}
	restored, err := l.RestoreVersion(n.ID, c.ID, saved.Revision, old.Revision)
	if err != nil || restored.Body != c.Body {
		t.Fatal("restore failed", err)
	}
	if _, err = l.RestoreVersion(n.ID, c.ID, saved.Revision, old.Revision); !errors.Is(err, ErrConflict) {
		t.Fatal("stale restore accepted", err)
	}
	for i := 0; i < 24; i++ {
		restored, err = l.SaveChapter(n.ID, c.ID, c.Title, fmt.Sprint(i), restored.Revision, false)
		if err != nil {
			t.Fatal(err)
		}
	}
	versions, _ = l.Versions(n.ID, c.ID)
	if len(versions) != 20 {
		t.Fatal("unbounded history", len(versions))
	}
	before := restored
	for i := 0; i < 8; i++ {
		restored, err = l.SaveChapter(n.ID, c.ID, c.Title, fmt.Sprint("auto", i), restored.Revision, true)
		if err != nil {
			t.Fatal(err)
		}
	}
	versions, _ = l.Versions(n.ID, c.ID)
	if versions[0].Revision != before.Revision {
		t.Fatal("autosave lost burst checkpoint", versions[0])
	}
	// Space failure must preserve both current content and version number.
	l.FreeSpace = func(string) (uint64, error) { return 0, nil }
	if _, err = l.SaveChapter(n.ID, c.ID, c.Title, "cannot persist", restored.Revision, false); err == nil {
		t.Fatal("low disk save accepted")
	}
	after, _ := l.Chapter(n.ID, c.ID)
	if after.Body != restored.Body || after.Revision != restored.Revision {
		t.Fatal("failed save mutated chapter")
	}
}
func TestNovelOrderTrashIsolationAndSnapshot(t *testing.T) {
	l := testLibrary(t)
	n, first := novelFixture(t, l)
	current, _ := l.Novel(n.ID)
	second, err := l.CreateChapter(n.ID, NewID(), "第二章", "正文二", current.Item.Revision)
	if err != nil {
		t.Fatal(err)
	}
	current, _ = l.Novel(n.ID)
	if err = l.ReorderChapters(n.ID, []string{second.ID, second.ID}, current.Item.Revision); err == nil {
		t.Fatal("duplicate order accepted")
	}
	if err = l.ReorderChapters(n.ID, []string{second.ID, first.ID}, current.Item.Revision); err != nil {
		t.Fatal(err)
	}
	if err = l.ReorderChapters(n.ID, []string{first.ID, second.ID}, current.Item.Revision); !errors.Is(err, ErrConflict) {
		t.Fatal("stale reorder accepted")
	}
	other, _ := l.CreateNovel(NewID(), "另一本", nil)
	if _, err = l.Chapter(other.ID, first.ID); !errors.Is(err, ErrMissing) {
		t.Fatal("chapter leaked across novels")
	}
	if err = l.NovelProgress(n.ID, first.ID); err != nil {
		t.Fatal(err)
	}
	if err = l.TrashChapter(n.ID, second.ID, second.Revision, false); err != nil {
		t.Fatal(err)
	}
	gone, _ := l.Chapter(n.ID, second.ID)
	if _, err = l.SaveChapter(n.ID, second.ID, second.Title, "overwrite trash", gone.Revision, false); !errors.Is(err, ErrMissing) {
		t.Fatal(err)
	}
	saved, err := l.SaveChapter(n.ID, first.ID, first.Title, "快照正文", first.Revision, false)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := filepath.Join(t.TempDir(), "snapshot")
	if err = l.Snapshot(context.Background(), snapshot); err != nil {
		t.Fatal(err)
	}
	if _, err = l.SaveChapter(n.ID, first.ID, first.Title, "快照之后", saved.Revision, false); err != nil {
		t.Fatal(err)
	}
	pinned, err := Open(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	defer pinned.Close()
	got, err := pinned.Chapter(n.ID, first.ID)
	if err != nil || got.Body != "快照正文" {
		t.Fatal("inconsistent checkpoint", got, err)
	}
	state, _ := pinned.Novel(n.ID)
	if state.Reading != first.ID || len(state.Trash) != 1 || state.Trash[0].ID != second.ID {
		t.Fatal("missing novel snapshot state", state)
	}
	if err = pinned.TrashChapter(n.ID, second.ID, gone.Revision, true); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	if _, err = pinned.ExportNovel(n.ID, &out); err != nil {
		t.Fatal(err)
	}
	if strings.Index(out.String(), "第二章") > strings.Index(out.String(), "第一章") || strings.Contains(out.String(), "快照之后") {
		t.Fatal("incorrect export", out.String())
	}
	if err = pinned.Trash(n.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err = pinned.Chapter(n.ID, first.ID); !errors.Is(err, ErrMissing) {
		t.Fatal("deleted parent still editable", err)
	}
	if err = pinned.Trash(n.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err = pinned.Chapter(n.ID, first.ID); err != nil {
		t.Fatal(err)
	}
}
func TestConcurrentChapterSavesHaveSingleWinner(t *testing.T) {
	l := testLibrary(t)
	n, c := novelFixture(t, l)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := l.SaveChapter(n.ID, c.ID, c.Title, fmt.Sprint("device", i), c.Revision, false)
			results <- err
		}(i)
	}
	wg.Wait()
	close(results)
	wins, conflicts := 0, 0
	for err := range results {
		if err == nil {
			wins++
		} else if errors.Is(err, ErrConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if wins != 1 || conflicts != 1 {
		t.Fatal(wins, conflicts)
	}
}
func TestNovelMigrationPreservesV1Library(t *testing.T) {
	l := testLibrary(t)
	n, c := novelFixture(t, l)
	_ = n
	_ = c
	// Simulate the released v1 schema, with existing ordinary content and settings.
	image := upload(t, l, "images", "existing.png", fixture(t))
	if err := l.Setup([]byte("old-hash")); err != nil {
		t.Fatal(err)
	}
	_, err := l.db.Exec("DROP TABLE novel_reading; DROP TABLE chapter_versions; DROP TABLE chapters; DELETE FROM items WHERE module='novels'; PRAGMA user_version=1")
	if err != nil {
		t.Fatal(err)
	}
	root := l.Root
	l.Close()
	migrated, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()
	got, err := migrated.Item(image.ID)
	if err != nil || got.Pages[0].SHA256 != image.Pages[0].SHA256 {
		t.Fatal("migration changed data", err)
	}
	backups, _ := filepath.Glob(filepath.Join(root, "staging", "before-novels-*.db"))
	if len(backups) != 1 {
		t.Fatal("missing migration snapshot", backups)
	}
	info, _ := os.Stat(backups[0])
	if info.Mode().Perm() != 0600 {
		t.Fatal("snapshot permissions")
	}
	old, err := sql.Open("sqlite3", backups[0]+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	var version int
	if err = old.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 1 {
		t.Fatal("backup not original schema", version, err)
	}
	var count int
	if err = old.QueryRow("SELECT COUNT(*) FROM items WHERE id=?", image.ID).Scan(&count); err != nil || count != 1 {
		t.Fatal("backup incomplete", err)
	}
	if err = migrated.Verify(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err = migrated.CreateNovel(NewID(), "迁移后的小说", nil); err != nil {
		t.Fatal(err)
	}
}
