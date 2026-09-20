package main

import (
	"context"
	"github.com/soraincloud/srics-next/internal/backup"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRecoveryPathBoundaries(t *testing.T) {
	root := t.TempDir()
	protected := filepath.Join(root, "library")
	if err := os.Mkdir(protected, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(protected, alias); err != nil {
		t.Fatal(err)
	}
	for _, dest := range []string{"relative", "/", protected, filepath.Join(protected, "restored"), filepath.Join(alias, "restored")} {
		if _, err := recoveryDirectory(dest, protected); err == nil {
			t.Fatal("unsafe restore location accepted", dest)
		}
	}
	if _, err := recoveryDirectory(filepath.Join(root, "restored"), protected); err != nil {
		t.Fatal(err)
	}
}
func TestRecoveryCannotActivateUnverifiedDirectory(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "config", "config.json")
	if err := os.Mkdir(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	c := localConfig{Data: filepath.Join(root, "missing-original"), Port: 19473}
	if err := writeConfig(path, c); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "unverified")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	if _, err := activateRecovery(context.Background(), path, dir); err == nil {
		t.Fatal("unverified data activated")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("failed activation changed config")
	}
}

func TestCancelledRecoveryLeavesPartialDirectoryWithoutSuccessReceipt(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "restic-test")
	id := strings.Repeat("a", 64)
	script := `#!/bin/sh
case " $* " in
*" snapshots "*) printf '%s\n' '[{"id":"` + id + `","time":"2026-09-20T10:00:00Z","hostname":"srics-library","tags":["library-v1"]}]'; exit 0;;
esac
while [ "$#" -gt 0 ]; do
 if [ "$1" = "--target" ]; then shift; target="$1"; break; fi
 shift
done
printf partial > "$target/partial"
exec sleep 30
`
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	client := backup.Client{Binary: binary, Repository: filepath.Join(root, "backup"), Password: "synthetic-password"}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dest := filepath.Join(root, "restore")
	done := make(chan error, 1)
	go func() { _, err := restoreVerified(ctx, client, id, dest); done <- err }()
	deadline := time.After(5 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(dest, "partial")); err == nil {
			break
		}
		select {
		case <-deadline:
			t.Fatal("restore did not start")
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled restore reported success")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("restore did not cancel")
	}
	if _, err := os.Stat(filepath.Join(dest, "partial")); err != nil {
		t.Fatal("partial directory was deleted")
	}
	if _, err := os.Stat(filepath.Join(dest, recoveryReceipt)); !os.IsNotExist(err) {
		t.Fatal("cancelled restore has success receipt")
	}
}
