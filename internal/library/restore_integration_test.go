//go:build integration

package library

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/soraincloud/srics-next/internal/backup"
	"github.com/soraincloud/srics-next/internal/vault"
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
	wrapped, e := l.PrepareVault("synthetic-independent-vault", "")
	if e != nil {
		t.Fatal(e)
	}
	if e = l.SetSetting("vault-key", wrapped); e != nil {
		t.Fatal(e)
	}
	key, e := vault.Unlock(wrapped, "synthetic-independent-vault")
	if e != nil {
		t.Fatal(e)
	}
	access := vault.NewAccess(context.Background(), key, time.Hour)
	private := privateUpload(t, l, access, "files", "private-recovery-code.txt", []byte("synthetic-private-recovery-code"))
	privatePhoto := privateUpload(t, l, access, "private", "private-backup.png", fixture(t))
	removedPrivate, e := l.ChangePrivate(context.Background(), access, private.ID, "", "trash", private.Revision)
	if e != nil {
		t.Fatal(e)
	}
	access.Lock()
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
	restoredWrapped, _ := restored.Setting("vault-key")
	restoredKey, e := vault.Unlock(restoredWrapped, "synthetic-independent-vault")
	if e != nil {
		t.Fatal(e)
	}
	restoredAccess := vault.NewAccess(context.Background(), restoredKey, time.Hour)
	defer restoredAccess.Lock()
	recovered, e := restored.ChangePrivate(context.Background(), restoredAccess, private.ID, "", "restore", removedPrivate.Revision)
	if e != nil {
		t.Fatal(e)
	}
	if !bytes.Equal(privateBytes(t, restored, restoredAccess, recovered, false), []byte("synthetic-private-recovery-code")) {
		t.Fatal("private restore corrupted")
	}
	recovered, e = restored.PrivateItem(restoredAccess, privatePhoto.ID)
	if e != nil {
		t.Fatal(e)
	}
	if !bytes.Equal(privateBytes(t, restored, restoredAccess, recovered, false), fixture(t)) || len(privateBytes(t, restored, restoredAccess, recovered, true)) == 0 {
		t.Fatal("private photo restore corrupted")
	}
	password, _ := restored.Setting("password")
	if string(password) != "synthetic-password-hash" {
		t.Fatal("login metadata missing")
	}
	if err = client.Restore(context.Background(), snapshot, restoredRoot); err == nil {
		t.Fatal("restore overwrote existing data")
	}
}
