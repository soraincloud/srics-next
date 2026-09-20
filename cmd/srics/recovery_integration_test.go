//go:build integration

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/soraincloud/srics-next/internal/backup"
	"github.com/soraincloud/srics-next/internal/library"
	"github.com/soraincloud/srics-next/internal/vault"
)

func TestNativeRecoveryAndActivationFromIndependentBackup(t *testing.T) {
	binary, err := exec.LookPath("restic")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	base := t.TempDir()
	root := filepath.Join(base, "original")
	if err = library.Create(root); err != nil {
		t.Fatal(err)
	}
	l, err := library.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if err = l.Setup([]byte("synthetic-login-hash")); err != nil {
		t.Fatal(err)
	}
	novel, err := l.CreateNovel(library.NewID(), "恢复测试", []string{"恢复"})
	if err != nil {
		t.Fatal(err)
	}
	chapter, err := l.CreateChapter(novel.ID, library.NewID(), "第一章", "合成正文", novel.Revision)
	if err != nil {
		t.Fatal(err)
	}
	wrapped, err := l.PrepareVault("synthetic-vault-password", "")
	if err != nil {
		t.Fatal(err)
	}
	if err = l.SetSetting("vault-key", wrapped); err != nil {
		t.Fatal(err)
	}
	key, err := vault.Unlock(wrapped, "synthetic-vault-password")
	if err != nil {
		t.Fatal(err)
	}
	access := vault.NewAccess(ctx, key, time.Minute)
	private, err := l.ReceivePrivate(ctx, access, library.NewID(), "files", "synthetic-private.txt", 7, bytes.NewReader([]byte("private")))
	if err != nil {
		t.Fatal(err)
	}
	access.Lock()
	stage := filepath.Join(root, "staging", "snapshot")
	if err = l.Snapshot(ctx, stage); err != nil {
		t.Fatal(err)
	}
	client := backup.Client{Binary: binary, Repository: filepath.Join(base, "backup"), Password: "synthetic-recovery-password"}
	if err = client.Init(ctx); err != nil {
		t.Fatal(err)
	}
	id, err := client.BackupLibrary(ctx, stage)
	if err != nil {
		t.Fatal(err)
	}
	sampleID, err := client.Backup(ctx, stage)
	if err != nil {
		t.Fatal(err)
	}
	snapshots, err := client.LibrarySnapshots(ctx)
	if err != nil || len(snapshots) != 1 || snapshots[0].ID != id {
		t.Fatal("snapshot filtering", snapshots, err)
	}
	if err = l.Close(); err != nil {
		t.Fatal(err)
	}
	if err = os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	wrong := client
	wrong.Password = "incorrect-recovery-password"
	untouched := filepath.Join(base, "untouched")
	if _, err = restoreVerified(ctx, wrong, id, untouched); err == nil {
		t.Fatal("wrong password accepted")
	}
	if _, err = os.Stat(untouched); !os.IsNotExist(err) {
		t.Fatal("wrong password created target")
	}
	if _, err = restoreVerified(ctx, client, sampleID, untouched); err == nil {
		t.Fatal("sample snapshot accepted")
	}
	restored := filepath.Join(base, "restored")
	result, err := restoreVerified(ctx, client, id, restored)
	if err != nil || !result.VaultPresent {
		t.Fatal("restore", result, err)
	}
	if _, err = restoreVerified(ctx, client, id, restored); err == nil {
		t.Fatal("existing target overwritten")
	}
	data, err := os.ReadFile(filepath.Join(restored, recoveryReceipt))
	if err != nil {
		t.Fatal(err)
	}
	var receipt recoveryResult
	if json.Unmarshal(data, &receipt) != nil || receipt.Snapshot != id || receipt.VerifiedAt.IsZero() {
		t.Fatal("missing receipt")
	}
	configPath := filepath.Join(base, "config", "config.json")
	if err = os.Mkdir(filepath.Dir(configPath), 0700); err != nil {
		t.Fatal(err)
	}
	if err = writeConfig(configPath, localConfig{Data: root, Port: 19473}); err != nil {
		t.Fatal(err)
	}
	recovered, err := library.Open(restored)
	if err != nil {
		t.Fatal(err)
	}
	body, err := recovered.Chapter(novel.ID, chapter.ID)
	if err != nil || body.Body != "合成正文" {
		t.Fatal("novel data lost", err)
	}
	if _, err = activateRecovery(ctx, configPath, restored); err == nil {
		t.Fatal("active restore directory adopted")
	}
	if err = recovered.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = activateRecovery(ctx, configPath, restored); err != nil {
		t.Fatal("new library not activated", err)
	}
	cfg, _, err := loadConfig(configPath)
	if err != nil || cfg.Data != result.Directory {
		t.Fatal("config not switched", cfg.Data, err)
	}
	// A changed private ciphertext must invalidate a later activation attempt.
	if err = writeConfig(configPath, localConfig{Data: root, Port: 19473}); err != nil {
		t.Fatal(err)
	}
	recovered, err = library.Open(restored)
	if err != nil {
		t.Fatal(err)
	}
	recoveredKey, err := vault.Unlock(wrapped, "synthetic-vault-password")
	if err != nil {
		t.Fatal(err)
	}
	recoveredAccess := vault.NewAccess(ctx, recoveredKey, time.Minute)
	item, err := recovered.PrivateItem(recoveredAccess, private.ID)
	if err != nil {
		t.Fatal(err)
	}
	reader, _, e := recovered.PrivateRead(ctx, recoveredAccess, item, false)
	if e != nil {
		t.Fatal(e)
	}
	plain, e := io.ReadAll(reader)
	reader.Close()
	if e != nil || string(plain) != "private" {
		t.Fatal("private contents lost", e)
	}
	recoveredAccess.Lock()
	recovered.Close()
	entries, err := os.ReadDir(filepath.Join(restored, "private-objects"))
	if err != nil || len(entries) == 0 {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(restored, "private-objects", entries[0].Name()), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = activateRecovery(ctx, configPath, restored); err == nil {
		t.Fatal("corrupt restored data activated")
	}
	cfg, _, _ = loadConfig(configPath)
	if cfg.Data != root {
		t.Fatal("failed activation changed source")
	}
}
