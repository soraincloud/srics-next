package library

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestDocumentOriginalsRetryConflictAndTrash(t *testing.T) {
	l, ctx := testLibrary(t), context.Background()
	body := "# 笔记\r\n\r\n原文  \r\n```go\r\nfmt.Println(\"hello\")\r\n```\r\n"
	id := NewID()
	d, err := l.SaveDocument(ctx, id, "  笔记  ", []string{"工作", "工作", " 测试 "}, body, 0)
	if err != nil || d.Item.Name != "笔记" || len(d.Item.Tags) != 2 {
		t.Fatal("create", d, err)
	}
	data, err := os.ReadFile(l.ObjectPath(d.Item.Pages[0].Object))
	if err != nil || !bytes.Equal(data, []byte(body)) {
		t.Fatal("Markdown original changed", err)
	}
	if retry, err := l.SaveDocument(ctx, id, d.Item.Name, d.Item.Tags, body, 0); err != nil || retry.Item.Revision != 1 {
		t.Fatal("create retry", err)
	}
	d, err = l.SaveDocument(ctx, id, "改名", []string{"分类"}, body+"追加\n", 1)
	if err != nil || d.Item.Revision != 2 {
		t.Fatal("update", err)
	}
	if _, err = l.SaveDocument(ctx, id, "过期", nil, "过期正文", 1); !errors.Is(err, ErrConflict) {
		t.Fatal("stale edit overwrote saved text", err)
	}
	if retry, err := l.SaveDocument(ctx, id, d.Item.Name, d.Item.Tags, d.Body, 1); err != nil || retry.Item.Revision != 2 {
		t.Fatal("update retry", err)
	}
	if err = l.Trash(id, false); err != nil {
		t.Fatal(err)
	}
	if _, err = l.Document(ctx, id); !errors.Is(err, ErrMissing) {
		t.Fatal("deleted document still readable", err)
	}
	if _, err = l.SaveDocument(ctx, id, "重建", nil, "", 0); !errors.Is(err, ErrConflict) {
		t.Fatal("deleted document ID was reused", err)
	}
	if err = l.Trash(id, true); err != nil {
		t.Fatal(err)
	}
	restored, err := l.Document(ctx, id)
	if err != nil || restored.Body != d.Body || restored.Item.Name != d.Item.Name {
		t.Fatal("restore lost document", err)
	}
	if err = l.Trash(id, false); err != nil {
		t.Fatal(err)
	}
	if err = l.Purge(id); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(l.ObjectPath(d.Item.Pages[0].Object)); !os.IsNotExist(err) {
		t.Fatal("purged Markdown object retained", err)
	}
}

func TestDocumentEmptyLimitsCorruptionAndAtomicFailure(t *testing.T) {
	l, ctx := testLibrary(t), context.Background()
	d, err := l.SaveDocument(ctx, NewID(), "空文档", nil, "", 0)
	if err != nil || d.Body != "" {
		t.Fatal("empty document", err)
	}
	if err = l.Verify(ctx); err != nil {
		t.Fatal("empty document could not be backed up", err)
	}
	for _, body := range []string{strings.Repeat("a", MaxDocumentBody+1), "has\x00nul", string([]byte{0xff})} {
		if _, err = l.SaveDocument(ctx, NewID(), "invalid", nil, body, 0); err == nil {
			t.Fatal("invalid body accepted")
		}
	}
	if _, err = l.CreateUpload(Upload{ID: NewID(), Module: "documents", Name: "not an image", Files: []UploadFile{{Name: "x.md", Size: 1}}}); err == nil {
		t.Fatal("image upload accepted documents")
	}
	if _, err = l.db.Exec("CREATE TRIGGER fail_document_save BEFORE UPDATE ON items BEGIN SELECT RAISE(ABORT, 'synthetic failure'); END"); err != nil {
		t.Fatal(err)
	}
	if _, err = l.SaveDocument(ctx, d.Item.ID, "覆盖", nil, "should not commit", 1); err == nil {
		t.Fatal("failed SQLite update reported success")
	}
	actual, err := l.Document(ctx, d.Item.ID)
	if err != nil || actual.Body != "" || actual.Item.Name != "空文档" || actual.Item.Revision != 1 {
		t.Fatal("failed update lost original", actual, err)
	}
	if _, err = l.db.Exec("DROP TRIGGER fail_document_save"); err != nil {
		t.Fatal(err)
	}
	d, err = l.SaveDocument(ctx, d.Item.ID, "原文", nil, "abc", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(l.ObjectPath(d.Item.Pages[0].Object), []byte("xyz"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = l.Document(ctx, d.Item.ID); err == nil {
		t.Fatal("same-size corrupt Markdown returned")
	}
	if _, err = l.SaveDocument(ctx, d.Item.ID, "override corruption", nil, "new", 2); err == nil {
		t.Fatal("corrupt saved text silently replaced")
	}
	if err = l.Verify(ctx); err == nil {
		t.Fatal("corrupt Markdown passed backup verification")
	}
}

func TestDocumentConcurrentSaveCollectionAndPinnedSnapshot(t *testing.T) {
	l, ctx := testLibrary(t), context.Background()
	d, err := l.SaveDocument(ctx, NewID(), "快照", []string{"恢复"}, "原版\n", 0)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := filepath.Join(t.TempDir(), "snapshot")
	if err = l.Snapshot(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 3)
	for _, body := range []string{"第一端\n", "第二端\n"} {
		wg.Add(1)
		go func(body string) {
			defer wg.Done()
			_, err := l.SaveDocument(ctx, d.Item.ID, d.Item.Name, d.Item.Tags, body, 1)
			results <- err
		}(body)
	}
	wg.Add(1)
	go func() { defer wg.Done(); results <- l.Collect() }()
	wg.Wait()
	close(results)
	conflicts := 0
	for err := range results {
		if errors.Is(err, ErrConflict) {
			conflicts++
		} else if err != nil {
			t.Fatal(err)
		}
	}
	if conflicts != 1 {
		t.Fatal("concurrent saves did not reject stale edit", conflicts)
	}
	if err = l.Collect(); err != nil {
		t.Fatal(err)
	}
	pinned, err := Open(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	defer pinned.Close()
	got, err := pinned.Document(ctx, d.Item.ID)
	if err != nil || got.Body != d.Body || got.Item.Revision != 1 {
		t.Fatal("snapshot Markdown changed", got, err)
	}
	if err = pinned.Verify(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestDocumentUpgradePreservesVersionFourLibrary(t *testing.T) {
	l := testLibrary(t)
	it := upload(t, l, "photos", "original.png", fixture(t))
	if _, err := l.db.Exec("PRAGMA user_version=4"); err != nil {
		t.Fatal(err)
	}
	root := l.Root
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	upgraded, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer upgraded.Close()
	if _, err = upgraded.Item(it.ID); err != nil {
		t.Fatal("upgrade lost old item", err)
	}
	var version int
	if err = upgraded.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 6 {
		t.Fatal("wrong version", version, err)
	}
	backups, _ := filepath.Glob(filepath.Join(root, "staging", "before-documents-*.db"))
	if len(backups) != 1 {
		t.Fatal("pre-upgrade index missing")
	}
	old, err := sql.Open("sqlite3", backups[0]+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	if err = old.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 4 {
		t.Fatal("pre-upgrade index changed", err)
	}
	var id string
	if err = old.QueryRow("SELECT id FROM items").Scan(&id); err != nil || id != it.ID {
		t.Fatal("pre-upgrade item missing", err)
	}
}
