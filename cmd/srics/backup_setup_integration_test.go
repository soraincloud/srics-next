//go:build integration

package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/soraincloud/srics-next/internal/library"
)

func TestBackupWizardGeneratedKeyRetryAndRestore(t *testing.T) {
	binary, err := exec.LookPath("restic")
	if err != nil {
		t.Skip("restic unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	root := t.TempDir()
	path := filepath.Join(root, "config.json")
	c := localConfig{Data: filepath.Join(root, "library"), Port: 19473}
	if err = library.Create(c.Data); err != nil {
		t.Fatal(err)
	}
	l, err := library.Open(c.Data)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if err = l.Setup([]byte("synthetic-login-hash")); err != nil {
		t.Fatal(err)
	}
	if err = writeConfig(path, c); err != nil {
		t.Fatal(err)
	}
	req := backupSetupRequest{Target: "local", Mode: "new", Repository: filepath.Join(root, "backup")}
	next, err := prepareBackupSetup(ctx, path, binary, c, l, req)
	if err != nil {
		t.Fatal(err)
	}
	secret, err := os.ReadFile(next.BackupPasswordFile)
	if err != nil || len(secret) != 43 {
		t.Fatal("random key missing", err)
	}
	info, _ := os.Stat(next.BackupPasswordFile)
	if info.Mode().Perm() != 0600 {
		t.Fatal("key permissions")
	}
	retry, err := prepareBackupSetup(ctx, path, binary, next, l, req)
	if err != nil {
		t.Fatal("retry failed", err)
	}
	unchanged, _ := os.ReadFile(retry.BackupPasswordFile)
	if !bytes.Equal(secret, unchanged) {
		t.Fatal("retry rotated key")
	}
	id, err := checkSetupBackup(ctx, next, l, binary, "local")
	if err != nil {
		t.Fatal(err)
	}
	client, err := configuredBackup(next, binary)
	if err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(root, "restored")
	if err = client.Restore(ctx, id, dest); err != nil {
		t.Fatal(err)
	}
	restored, err := library.Open(dest)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := restored.Setting("password")
	restored.Close()
	if err != nil || string(hash) != "synthetic-login-hash" {
		t.Fatal("database missing in backup")
	}
	before, _ := os.ReadFile(path)
	foreign := backupSetupRequest{Target: "local", Mode: "existing", Repository: req.Repository, Password: "wrong-synthetic-password"}
	if _, err = prepareBackupSetup(ctx, path, binary, next, l, foreign); err == nil {
		t.Fatal("wrong password accepted")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("failed connection overwrote working configuration")
	}
	foreign.Password = string(secret)
	imported, err := prepareBackupSetup(ctx, path, binary, next, l, foreign)
	if err != nil {
		t.Fatal("correcting imported password failed", err)
	}
	importedKey, _ := os.ReadFile(imported.BackupPasswordFile)
	if !bytes.Equal(secret, importedKey) {
		t.Fatal("import altered password")
	}
	if err = os.Rename(req.Repository, req.Repository+"-offline"); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"current", "new"} {
		req.Mode = mode
		if _, err = prepareBackupSetup(ctx, path, binary, next, l, req); err == nil {
			t.Fatal("missing initialized backup recreated", mode)
		}
		if _, err = os.Stat(req.Repository); !os.IsNotExist(err) {
			t.Fatal("missing repository recreated")
		}
	}
	saved, _ := os.ReadFile(next.BackupPasswordFile)
	if !bytes.Equal(saved, secret) {
		t.Fatal("failure lost key")
	}
}
