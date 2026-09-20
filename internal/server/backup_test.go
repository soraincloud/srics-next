package server

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/soraincloud/srics-next/internal/backup"
	"github.com/soraincloud/srics-next/internal/library"
)

func TestDailyScheduleCatchupRetryAndDST(t *testing.T) {
	zone, _ := time.LoadLocation("America/New_York")
	now := time.Date(2026, 3, 8, 12, 0, 0, 0, zone)
	due := time.Date(2026, 3, 8, 3, 0, 0, 0, zone)
	if got := nextBackup(now, "03:00", backupRecord{}); !got.Equal(now) {
		t.Fatal("missed run not caught up", got)
	}
	success := backupRecord{Status: "passed", SavedAt: due.Format(time.RFC3339)}
	if got := nextBackup(now, "03:00", success); !got.Equal(due.AddDate(0, 0, 1)) {
		t.Fatal("daily schedule drifted", got)
	}
	beforeDST := time.Date(2026, 3, 7, 12, 0, 0, 0, zone)
	previous := backupRecord{Status: "passed", SavedAt: time.Date(2026, 3, 7, 3, 0, 0, 0, zone).Format(time.RFC3339)}
	if got := nextBackup(beforeDST, "03:00", previous); !got.Equal(due) {
		t.Fatal("DST shifted local execution time", got)
	}
	for _, status := range []string{"failed", "running"} {
		r := backupRecord{Status: status, AttemptedAt: now.Add(-10 * time.Minute).Format(time.RFC3339)}
		if got := nextBackup(now, "03:00", r); !got.Equal(now.Add(50 * time.Minute)) {
			t.Fatal("retry too soon", got)
		}
		r.FinishedAt = now.Add(-5 * time.Minute).Format(time.RFC3339)
		if got := nextBackup(now, "03:00", r); !got.Equal(now.Add(55 * time.Minute)) {
			t.Fatal("retry ignored completion time", got)
		}
	}
}

func TestScheduledFailurePersistsAndDoesNotRecreateRepository(t *testing.T) {
	root := t.TempDir()
	if err := library.Create(filepath.Join(root, "library")); err != nil {
		t.Fatal(err)
	}
	l, err := library.Open(filepath.Join(root, "library"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := &LibraryAPI{store: l, ctx: ctx, backup: backup.Client{Repository: filepath.Join(root, "missing-disk", "backup")}}
	if err = a.startBackup(); err != nil {
		t.Fatal(err)
	}
	a.wg.Wait()
	record, err := a.readBackupRecord()
	if err != nil || record.Status != "failed" || record.SavedAt != "" {
		t.Fatalf("false success: %+v %v", record, err)
	}
	if _, err = os.Stat(filepath.Join(root, "missing-disk")); !os.IsNotExist(err) {
		t.Fatal("created absent drive")
	}
	if !nextBackup(time.Now(), "03:00", record).After(time.Now()) {
		t.Fatal("failure immediately retried")
	}
	// Previously initialized repositories must never be recreated after removal.
	a.backup.Repository = filepath.Join(root, "previous-backup")
	record = backupRecord{Repository: a.backupRepositoryID(), Initialized: true}
	if err = a.saveBackupRecord(record); err != nil {
		t.Fatal(err)
	}
	if err = a.startBackup(); err != nil {
		t.Fatal(err)
	}
	a.wg.Wait()
	record, err = a.readBackupRecord()
	if err != nil || record.Status != "failed" {
		t.Fatal("missing repository accepted", err)
	}
	if _, err = os.Stat(a.backup.Repository); !os.IsNotExist(err) {
		t.Fatal("recreated lost repository")
	}
	a.backup.Repository = filepath.Join(root, "another-backup")
	record, err = a.readBackupRecord()
	if err != nil || record.Status != "" {
		t.Fatal("old destination status leaked to new one")
	}
}
