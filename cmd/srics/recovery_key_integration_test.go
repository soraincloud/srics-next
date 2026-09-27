//go:build integration

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/soraincloud/srics-next/internal/backup"
	"github.com/soraincloud/srics-next/internal/library"
	"github.com/soraincloud/srics-next/internal/vault"
	"golang.org/x/crypto/bcrypt"
)

// Simulates total loss of the original machine. Only a copied repository and the
// separately exported key survive; no original password is used during recovery.
func TestRecoveryKeySurvivesLossOfLibraryConfigAndPasswords(t *testing.T) {
	binary, err := exec.LookPath("restic")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	base := t.TempDir()
	machine := filepath.Join(base, "lost-machine")
	if err = os.Mkdir(machine, 0700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(machine, "config", "config.json")
	if err = os.Mkdir(filepath.Dir(configPath), 0700); err != nil {
		t.Fatal(err)
	}
	c := localConfig{Data: filepath.Join(machine, "library"), Port: 19473, BackupRepository: filepath.Join(base, "backup"), BackupPasswordFile: filepath.Join(machine, "password")}
	if err = os.WriteFile(c.BackupPasswordFile, []byte("synthetic-original-backup-password"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = writeConfig(configPath, c); err != nil {
		t.Fatal(err)
	}
	if err = library.Create(c.Data); err != nil {
		t.Fatal(err)
	}
	l, err := library.Open(c.Data)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if err = l.Setup([]byte("synthetic-old-login-hash")); err != nil {
		t.Fatal(err)
	}
	novel, err := l.CreateNovel(library.NewID(), "应急小说", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = l.CreateChapter(novel.ID, library.NewID(), "第一章", "整机丢失后仍可取回的正文", novel.Revision); err != nil {
		t.Fatal(err)
	}
	wrapped, err := l.PrepareVault("synthetic-original-vault-password", "")
	if err != nil {
		t.Fatal(err)
	}
	if err = l.SetSetting("vault-key", wrapped); err != nil {
		t.Fatal(err)
	}
	identity, err := vault.Unlock(wrapped, "synthetic-original-vault-password")
	if err != nil {
		t.Fatal(err)
	}
	a := vault.NewAccess(ctx, identity, time.Minute)
	plain := bytes.Repeat([]byte("synthetic-private-original\x00"), 1000)
	private, err := l.ReceivePrivate(ctx, a, library.NewID(), "files", "secret.bin", int64(len(plain)), bytes.NewReader(plain))
	a.Lock()
	if err != nil {
		t.Fatal(err)
	}
	client, err := configuredBackup(c, binary)
	if err != nil {
		t.Fatal(err)
	}
	if err = client.Init(ctx); err != nil {
		t.Fatal(err)
	}
	beforeStage := filepath.Join(c.Data, "staging", "before-key")
	if err = l.Snapshot(ctx, beforeStage); err != nil {
		t.Fatal(err)
	}
	oldID, err := client.BackupLibrary(ctx, beforeStage)
	if err != nil {
		t.Fatal(err)
	}
	req := recoveryKeyRequest{Target: "local", File: filepath.Join(base, "offline-recovery.json"), VaultPassword: "wrong-vault-password"}
	if _, err = generateRecoveryKey(ctx, c, configPath, client, l, req); err == nil {
		t.Fatal("wrong vault password accepted")
	}
	if _, err = os.Stat(req.File); !os.IsNotExist(err) {
		t.Fatal("failed generation left secret file")
	}
	req.VaultPassword = "synthetic-original-vault-password"
	unsafe := req
	unsafe.File = filepath.Join(c.Data, "leaked-key.json")
	if _, err = generateRecoveryKey(ctx, c, configPath, client, l, unsafe); err == nil {
		t.Fatal("secret export into library allowed")
	}
	alias := filepath.Join(base, "alias")
	if err = os.Symlink(c.BackupRepository, alias); err != nil {
		t.Fatal(err)
	}
	unsafe.File = filepath.Join(alias, "leaked-key.json")
	if _, err = generateRecoveryKey(ctx, c, configPath, client, l, unsafe); err == nil {
		t.Fatal("symlink export into repository allowed")
	}
	out, err := generateRecoveryKey(ctx, c, configPath, client, l, req)
	if err != nil {
		t.Fatal(err)
	}
	if out.Info.State != "exported" || out.Info.VerifiedAt != nil {
		t.Fatal("unverified key shown as enabled")
	}
	key, err := readRecoveryKey(req.File)
	if err != nil {
		t.Fatal(err)
	}
	fileBefore, _ := os.ReadFile(req.File)
	if _, err = generateRecoveryKey(ctx, c, configPath, client, l, req); err == nil {
		t.Fatal("existing key overwritten")
	}
	fileAfter, _ := os.ReadFile(req.File)
	if !bytes.Equal(fileBefore, fileAfter) {
		t.Fatal("existing recovery file changed")
	}
	response, _ := json.Marshal(out)
	record, _ := l.Setting("recovery-key-" + key.ID)
	if bytes.Contains(response, []byte(key.Secret)) || bytes.Contains(record, []byte(key.Secret)) || bytes.Contains(record, []byte(identity.String())) {
		t.Fatal("secret leaked into response or library")
	}
	if bytes.Contains(fileBefore, []byte(req.VaultPassword)) || bytes.Contains(fileBefore, []byte(client.Password)) {
		t.Fatal("recovery file stored original passwords")
	}
	source := recoverySource{Target: "local", Repository: c.BackupRepository, RecoveryKeyFile: req.File}
	if _, err = source.client(ctx, binary); err == nil {
		t.Fatal("export alone enabled key")
	}
	cancelled, stop := context.WithCancel(ctx)
	stop()
	if _, err = confirmRecoveryKey(cancelled, c, configPath, client, l, req); err == nil {
		t.Fatal("cancelled confirmation succeeded")
	}
	out, err = confirmRecoveryKey(ctx, c, configPath, client, l, req)
	if err != nil {
		t.Fatal(err)
	}
	if out.Info.State != "verified" || out.Info.Snapshot == "" || out.Info.VerifiedAt == nil {
		t.Fatal("missing successful backup evidence")
	}
	keyClient, err := source.client(ctx, binary)
	if err != nil {
		t.Fatal(err)
	}
	if err = client.AddRecoveryPassword(ctx, key.Secret, key.RepositoryID); err != nil {
		t.Fatal("idempotent key registration", err)
	}
	keys, err := os.ReadDir(filepath.Join(c.BackupRepository, "keys"))
	if err != nil || len(keys) != 2 {
		t.Fatal("retry duplicated repository key", err)
	}
	snapshots, err := keyClient.LibrarySnapshots(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, snapshot := range snapshots {
		if snapshot.ID == oldID && slices.Contains(snapshot.RecoveryKeys, key.ID) {
			t.Fatal("old backup incorrectly marked recoverable")
		}
		if snapshot.ID == out.Info.Snapshot && !slices.Contains(snapshot.RecoveryKeys, key.ID) {
			t.Fatal("new snapshot missing recovery label")
		}
	}
	// Future scheduled/manual snapshots also keep the key marker; password changes
	// rewrap the same identity without invalidating the independent recovery key.
	newWrapped, err := l.PrepareVault("synthetic-changed-vault-password", req.VaultPassword)
	if err != nil {
		t.Fatal(err)
	}
	if err = l.SetSetting("vault-key", newWrapped); err != nil {
		t.Fatal(err)
	}
	a, err = recoveryKeyAccess(ctx, l, req.File)
	if err != nil {
		t.Fatal("rotation broke recovery", err)
	}
	a.Lock()
	future := filepath.Join(c.Data, "staging", "future")
	if err = l.Snapshot(ctx, future); err != nil {
		t.Fatal(err)
	}
	futureID, err := client.BackupLibrary(ctx, future)
	if err != nil {
		t.Fatal(err)
	}
	snapshots, err = client.LibrarySnapshots(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if snapshots[0].ID != futureID || !slices.Contains(snapshots[0].RecoveryKeys, key.ID) {
		t.Fatal("future snapshot lost recovery marker")
	}
	if _, err = client.RepositoryID(ctx); err != nil {
		t.Fatal("normal password stopped working")
	}
	if err = l.Close(); err != nil {
		t.Fatal(err)
	}
	if err = os.RemoveAll(machine); err != nil {
		t.Fatal(err)
	}
	// A restored/copied repository is independent of its previous filesystem path.
	moved := filepath.Join(base, "surviving-copy")
	if err = os.Rename(c.BackupRepository, moved); err != nil {
		t.Fatal(err)
	}
	source.Repository = moved
	keyClient, err = source.client(ctx, binary)
	if err != nil {
		t.Fatal(err)
	}
	client = backup.Client{}
	req.VaultPassword = ""
	wrapped = nil
	identity = nil
	restored := filepath.Join(base, "restored")
	if _, err = restoreVerified(ctx, keyClient, futureID, restored); err != nil {
		t.Fatal(err)
	}
	newConfigPath := filepath.Join(base, "new-machine", "config.json")
	if err = os.Mkdir(filepath.Dir(newConfigPath), 0700); err != nil {
		t.Fatal(err)
	}
	r := recoveryRequest{Source: source, Directory: restored}
	items, err := recoveryExportRequest(ctx, newConfigPath, r)
	if err != nil || len(items.Items) != 2 {
		t.Fatal("key-only listing", err)
	}
	r.ItemID, r.Private, r.ExportDirectory = private.ID, true, base
	exported, err := recoveryExportRequest(ctx, newConfigPath, r)
	if err != nil {
		t.Fatal(err)
	}
	actual, err := os.ReadFile(exported.Exported)
	if err != nil || !bytes.Equal(actual, plain) {
		t.Fatal("private original bytes differ", err)
	}
	r.ItemID, r.Private = novel.ID, false
	exported, err = recoveryExportRequest(ctx, newConfigPath, r)
	if err != nil {
		t.Fatal(err)
	}
	actual, err = os.ReadFile(exported.Exported)
	if err != nil || !bytes.Contains(actual, []byte("整机丢失后仍可取回的正文")) {
		t.Fatal("novel lost", err)
	}
	r.NewPassword, r.NewVaultPassword = "synthetic-new-login-password", "short"
	if _, err = resetRecoveryPasswords(ctx, newConfigPath, r); err == nil {
		t.Fatal("invalid vault reset accepted")
	}
	l, err = library.Open(restored)
	if err != nil {
		t.Fatal(err)
	}
	hash, _ := l.Setting("password")
	l.Close()
	if string(hash) != "synthetic-old-login-hash" {
		t.Fatal("failed reset partially changed login")
	}
	r.NewVaultPassword = "synthetic-new-vault-password"
	reset, err := resetRecoveryPasswords(ctx, newConfigPath, r)
	if err != nil || !reset.PasswordsReset {
		t.Fatal("passwordless reset", err)
	}
	l, err = library.Open(restored)
	if err != nil {
		t.Fatal(err)
	}
	hash, _ = l.Setting("password")
	if bcrypt.CompareHashAndPassword(hash, []byte(r.NewPassword)) != nil {
		t.Fatal("new login password unusable")
	}
	a, err = recoveryAccess(ctx, l, r.NewVaultPassword)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = l.PrivateItems(ctx, a); err != nil {
		t.Fatal(err)
	}
	a.Lock()
	l.Close()
	if _, err = activateRecovery(ctx, newConfigPath, restored); err != nil {
		t.Fatal(err)
	}
	active, _, err := loadConfig(newConfigPath)
	resolvedRestored, _ := resolvedPath(restored)
	if err != nil || active.Data != resolvedRestored {
		t.Fatal("new machine activation failed", err)
	}
	// A foreign recovery file cannot use the reset endpoint as a password bypass.
	foreign := *key
	foreign.LibraryID = library.NewID()
	foreignPath := filepath.Join(base, "foreign.json")
	b, _ := json.Marshal(foreign)
	if err = os.WriteFile(foreignPath, b, 0600); err != nil {
		t.Fatal(err)
	}
	l, err = library.Open(restored)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if _, err = recoveryKeyAccess(ctx, l, foreignPath); err == nil {
		t.Fatal("foreign library key accepted")
	}
	if strings.Contains(string(response), "AGE-SECRET-KEY") {
		t.Fatal("private key in public status")
	}
}
