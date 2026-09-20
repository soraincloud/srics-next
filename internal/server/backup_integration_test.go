//go:build integration

package server

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"

	"github.com/soraincloud/srics-next/internal/backup"
	"github.com/soraincloud/srics-next/internal/library"
	"github.com/soraincloud/srics-next/internal/media"
)

func TestAutomaticBackupAndRestore(t *testing.T) {
	binary, err := exec.LookPath("restic")
	if err != nil {
		t.Skip("restic unavailable")
	}
	root := t.TempDir()
	data := filepath.Join(root, "library")
	if err = library.Create(data); err != nil {
		t.Fatal(err)
	}
	l, err := library.Open(data)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	s := New(ctx, "127.0.0.1:19473", fstest.MapFS{}, nil)
	b := backup.Client{Binary: binary, Repository: filepath.Join(root, "backup"), Password: "synthetic-scheduled-backup"}
	s.EnableLibrary(l, media.Converter{}, b)
	s.StartBackupSchedule("03:00")
	defer s.Wait()
	defer cancel()
	var record backupRecord
	for {
		record, err = s.library.readBackupRecord()
		if err != nil {
			t.Fatal(err)
		}
		if record.Status == "passed" || record.Status == "failed" {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(50 * time.Millisecond):
		}
	}
	if record.Status != "passed" || !record.ReadVerified || !record.Initialized || record.Snapshot == "" {
		t.Fatalf("incomplete backup: %+v", record)
	}
	if !nextBackup(time.Now(), "03:00", record).After(time.Now()) {
		t.Fatal("completed backup still due")
	}
	dest := filepath.Join(root, "restored")
	if err = b.Restore(ctx, record.Snapshot, dest); err != nil {
		t.Fatal(err)
	}
	restored, err := library.Open(dest)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if err = restored.Verify(ctx); err != nil {
		t.Fatal(err)
	}
}
