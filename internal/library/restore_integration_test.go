//go:build integration

package library

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/soraincloud/srics-next/internal/backup"
)

func TestRealLibraryEncryptedBackupAndIndependentRestore(t *testing.T) {
	binary, err := exec.LookPath("restic")
	if err != nil {
		t.Fatal("restic is required", err)
	}
	base := t.TempDir()
	root := filepath.Join(base, "original")
	if err = Create(root); err != nil {
		t.Fatal(err)
	}
	l, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	first := upload(t, l, "images", "same.png", fixture(t))
	photo := upload(t, l, "photos", "same.png", fixture(t))
	if err = l.Trash(photo.ID, false); err != nil {
		t.Fatal(err)
	}
	if err = l.Setup([]byte("synthetic-password-hash")); err != nil {
		t.Fatal(err)
	}
	novel, chapter := novelFixture(t, l)
	saved, err := l.SaveChapter(novel.ID, chapter.ID, chapter.Title, "加密备份中的小说正文", chapter.Revision, false)
	if err != nil {
		t.Fatal(err)
	}
	current, _ := l.Novel(novel.ID)
	removed, err := l.CreateChapter(novel.ID, NewID(), "已删除章节", "仍可恢复", current.Item.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if err = l.TrashChapter(novel.ID, removed.ID, removed.Revision, false); err != nil {
		t.Fatal(err)
	}
	if err = l.NovelProgress(novel.ID, chapter.ID); err != nil {
		t.Fatal(err)
	}
	stage := filepath.Join(root, "staging", "snapshot")
	if err = l.Snapshot(context.Background(), stage); err != nil {
		t.Fatal(err)
	}
	client := backup.Client{Binary: binary, Repository: filepath.Join(base, "backup"), Password: "separate-synthetic-backup-material"}
	if err = client.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	snapshot, err := client.BackupLibrary(context.Background(), stage)
	if err != nil {
		t.Fatal(err)
	}
	if err = client.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	l.Close()
	if err = os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	restoredRoot := filepath.Join(base, "restored")
	if err = client.Restore(context.Background(), snapshot, restoredRoot); err != nil {
		t.Fatal(err)
	}
	restored, err := Open(restoredRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if err = restored.Verify(context.Background()); err != nil {
		t.Fatal(err)
	}
	it, err := restored.Item(first.ID)
	if err != nil || it.Name != first.Name || it.Pages[0].SHA256 != first.Pages[0].SHA256 {
		t.Fatal("image reference lost", err)
	}
	trash, _ := restored.Items("all", true)
	if len(trash) != 1 || trash[0].ID != photo.ID {
		t.Fatal("trash missing after restore")
	}
	restoredChapter, err := restored.Chapter(novel.ID, chapter.ID)
	if err != nil || restoredChapter.Body != saved.Body || restoredChapter.Revision != saved.Revision {
		t.Fatal("novel lost after independent restore", err)
	}
	restoredNovel, err := restored.Novel(novel.ID)
	if err != nil || restoredNovel.Reading != chapter.ID || len(restoredNovel.Trash) != 1 {
		t.Fatal("novel structure lost", err)
	}
	versions, err := restored.Versions(novel.ID, chapter.ID)
	if err != nil || len(versions) != 1 {
		t.Fatal("chapter history lost", err)
	}
	old, err := restored.RestoreVersion(novel.ID, chapter.ID, saved.Revision, versions[0].Revision)
	if err != nil || old.Body != chapter.Body {
		t.Fatal("history cannot be restored", err)
	}
	if err = restored.TrashChapter(novel.ID, removed.ID, restoredNovel.Trash[0].Revision, true); err != nil {
		t.Fatal("deleted chapter cannot be restored", err)
	}
	password, _ := restored.Setting("password")
	if string(password) != "synthetic-password-hash" {
		t.Fatal("login metadata missing")
	}
	if err = client.Restore(context.Background(), snapshot, restoredRoot); err == nil {
		t.Fatal("restore overwrote existing data")
	}
}
