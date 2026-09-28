package server

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/soraincloud/srics-next/internal/backup"
	"github.com/soraincloud/srics-next/internal/library"
)

func TestBackupTargetsHaveIndependentStatusAndSharedExecutionLock(t *testing.T) {
	root := t.TempDir()
	data := filepath.Join(root, "library")
	if err := library.Create(data); err != nil {
		t.Fatal(err)
	}
	l, err := library.Open(data)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	a := &LibraryAPI{store: l, ctx: context.Background(), backup: backup.Client{Repository: filepath.Join(root, "local")}, cloud: backup.Client{Repository: "s3:https://storage.example.com/bucket/prefix"}}
	if err = a.saveBackupRecord(backupRecord{Repository: a.backupRepositoryID(), Status: "passed", Snapshot: "local-snapshot"}); err != nil {
		t.Fatal(err)
	}
	if err = a.saveBackupRecord(backupRecord{Repository: a.backupRepositoryID("cloud"), Status: "failed", Error: "cloud failed"}, "cloud"); err != nil {
		t.Fatal(err)
	}
	local, _ := a.readBackupRecord()
	cloud, _ := a.readBackupRecord("cloud")
	if local.Status != "passed" || cloud.Status != "failed" || cloud.Snapshot != "" {
		t.Fatal("backup targets mixed")
	}
	a.backupActive = true
	a.backupActiveTarget = "local"
	if err = a.startBackup("cloud"); err != library.ErrConflict {
		t.Fatal("concurrent backup accepted", err)
	}
	status, err := a.backupStatus("cloud")
	if err != nil {
		t.Fatal(err)
	}
	view := status.(map[string]any)
	if view["running"] != false || view["busy"] != true {
		t.Fatal("wrong per-target running status")
	}
	encoded, _ := json.Marshal(status)
	if strings.Contains(string(encoded), "secretAccessKey") || strings.Contains(string(encoded), "passwordFile") {
		t.Fatal("secrets leaked to status")
	}
}

func TestFailedReadCheckPreservesLastSuccessAndSkipsRetention(t *testing.T) {
	root := t.TempDir()
	data := filepath.Join(root, "library")
	if err := library.Create(data); err != nil {
		t.Fatal(err)
	}
	l, err := library.Open(data)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	repo := filepath.Join(root, "repo")
	if err = os.Mkdir(repo, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(repo, "config"), []byte("synthetic-repository"), 0600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(root, "restic-test")
	script := `#!/bin/sh
case " $* " in
*" backup "*) printf '%s\n' '{"message_type":"summary","snapshot_id":"` + strings.Repeat("a", 64) + `"}'; exit 0;;
*" check "*) exit 1;;
esac
printf unexpected-command > "$(dirname "$0")/unexpected"
exit 1
`
	if err = os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	a := &LibraryAPI{store: l, ctx: context.Background(), backup: backup.Client{Binary: binary, Repository: repo, Password: "synthetic-backup-password"}, retention: backup.Retention{Enabled: true, Daily: 1}}
	previous := backupRecord{Repository: a.backupRepositoryID(), Initialized: true, Status: "passed", Snapshot: strings.Repeat("b", 64), ReadVerified: true, SavedAt: "2026-09-27T01:00:00Z", VerifiedAt: "2026-09-27T01:05:00Z"}
	if err = a.saveBackupRecord(previous); err != nil {
		t.Fatal(err)
	}
	if err = a.startBackup(); err != nil {
		t.Fatal(err)
	}
	a.wg.Wait()
	actual, err := a.readBackupRecord()
	if err != nil {
		t.Fatal(err)
	}
	if actual.Status != "failed" || actual.Error == "" || actual.Snapshot != previous.Snapshot || actual.SavedAt != previous.SavedAt || actual.VerifiedAt != previous.VerifiedAt {
		t.Fatal("failed read verification overwrote last known good restore point", actual)
	}
	if _, err = os.Stat(filepath.Join(root, "unexpected")); !os.IsNotExist(err) {
		t.Fatal("failed check continued to repository cleanup")
	}
}

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

func TestBackupHistoryPaginationAndTargetIsolation(t *testing.T) {
	entries := make([]backup.Snapshot, 53)
	for i := range entries {
		entries[i] = backup.Snapshot{ID: fmt.Sprintf("%064x", i+1), Time: time.Now().Add(-time.Duration(i) * time.Minute)}
	}
	a := &LibraryAPI{backup: backup.Client{Repository: "/local-repo"}, cloud: backup.Client{Repository: "s3:https://cloud.test/bucket/prefix"}, backupHistories: map[string]snapshotCache{
		"local": {snapshots: entries, until: time.Now().Add(time.Minute)}, "cloud": {snapshots: []backup.Snapshot{}, until: time.Now().Add(time.Minute)}}}
	first, err := a.backupHistory(context.Background(), "local", "")
	if err != nil {
		t.Fatal(err)
	}
	page := first.(map[string]any)
	if len(page["snapshots"].([]backup.Snapshot)) != 50 || page["next"] != entries[49].ID {
		t.Fatal("first page", page)
	}
	next, err := a.backupHistory(context.Background(), "local", page["next"].(string))
	if err != nil {
		t.Fatal(err)
	}
	page = next.(map[string]any)
	if len(page["snapshots"].([]backup.Snapshot)) != 3 || page["next"] != "" {
		t.Fatal("last page", page)
	}
	cloud, err := a.backupHistory(context.Background(), "cloud", "")
	if err != nil || len(cloud.(map[string]any)["snapshots"].([]backup.Snapshot)) != 0 {
		t.Fatal("local history leaked to cloud", cloud, err)
	}
	if _, err = a.backupHistory(context.Background(), "local", "missing"); err == nil {
		t.Fatal("stale cursor accepted")
	}
	a.backupHistoryMu.Lock()
	_, err = a.backupHistory(context.Background(), "local", "")
	a.backupHistoryMu.Unlock()
	if err != library.ErrConflict {
		t.Fatal("parallel restic list permitted", err)
	}
}
