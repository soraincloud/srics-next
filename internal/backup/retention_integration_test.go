//go:build integration

package backup

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestRetentionPrunesOnlyLibraryAndRestoresLatest(t *testing.T) {
	binary, err := exec.LookPath("restic")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	root := t.TempDir()
	stage := filepath.Join(root, "stage")
	os.Mkdir(stage, 0700)
	c := Client{Binary: binary, Repository: filepath.Join(root, "repo"), Password: "synthetic-retention-password"}
	if err = c.Init(ctx); err != nil {
		t.Fatal(err)
	}
	write := func(s string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(stage, "file"), []byte(s), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("sample")
	sample, err := c.Backup(ctx, stage)
	if err != nil {
		t.Fatal(err)
	}
	write("old")
	if _, err = c.BackupLibrary(ctx, stage); err != nil {
		t.Fatal(err)
	}
	p := Retention{Enabled: true, Daily: 1}
	stale, err := c.PlanRetention(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Millisecond * 20)
	write("latest")
	latest, err := c.BackupLibrary(ctx, stage)
	if err != nil {
		t.Fatal(err)
	}
	if err = c.ApplyRetention(ctx, p, stale.Token); err == nil {
		t.Fatal("stale plan applied")
	}
	plan, err := c.PlanRetention(ctx, p)
	if err != nil || len(plan.Remove) != 1 || len(plan.Keep) != 1 {
		t.Fatal(plan, err)
	}
	if err = c.ApplyRetention(ctx, p, plan.Token); err != nil {
		t.Fatal(err)
	}
	snapshots, err := c.LibrarySnapshots(ctx)
	if err != nil || len(snapshots) != 1 || snapshots[0].ID != latest {
		t.Fatal(snapshots, err)
	}
	for id, body := range map[string]string{sample: "sample", latest: "latest"} {
		dest := filepath.Join(root, id)
		if err = c.Restore(ctx, id, dest); err != nil {
			t.Fatal(err)
		}
		b, e := os.ReadFile(filepath.Join(dest, "file"))
		if e != nil || string(b) != body {
			t.Fatal(string(b), e)
		}
	}
	// A retry after forget but before successful prune must still reclaim/check the repo.
	plan, err = c.PlanRetention(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	if err = c.ApplyRetention(ctx, p, plan.Token); err != nil {
		t.Fatal(err)
	}
}
