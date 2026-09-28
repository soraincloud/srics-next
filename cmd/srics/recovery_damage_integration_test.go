//go:build integration

package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/soraincloud/srics-next/internal/backup"
	"github.com/soraincloud/srics-next/internal/library"
	"github.com/soraincloud/srics-next/internal/vault"
)

// A restic-valid snapshot can still contain a broken application recovery
// wrapper. Do not publish a successful key-based restore before actually using it.
func TestRecoveryRejectsUnreadableVaultBeforeSuccessReceipt(t *testing.T) {
	binary, err := exec.LookPath("restic")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	base := t.TempDir()
	configPath := filepath.Join(base, "config", "config.json")
	if err = os.Mkdir(filepath.Dir(configPath), 0700); err != nil {
		t.Fatal(err)
	}
	c := localConfig{Data: filepath.Join(base, "library"), Port: 19473, BackupRepository: filepath.Join(base, "backup"), BackupPasswordFile: filepath.Join(base, "password.txt")}
	if err = os.WriteFile(c.BackupPasswordFile, []byte("synthetic-backup-password"), 0600); err != nil {
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
	identity, wrapped, err := vault.Create("synthetic-vault-password")
	if err != nil {
		t.Fatal(err)
	}
	if err = l.SetSetting("vault-key", wrapped); err != nil {
		t.Fatal(err)
	}
	access := vault.NewAccess(ctx, identity, time.Minute)
	private, err := l.ReceivePrivate(ctx, access, library.NewID(), "files", "private.txt", 7, bytes.NewBufferString("private"))
	access.Lock()
	if err != nil {
		t.Fatal(err)
	}
	client := backup.Client{Binary: binary, Repository: c.BackupRepository, Password: "synthetic-backup-password"}
	if err = client.Init(ctx); err != nil {
		t.Fatal(err)
	}
	req := recoveryKeyRequest{Target: "local", File: filepath.Join(base, "offline-key.json"), VaultPassword: "synthetic-vault-password"}
	if _, err = generateRecoveryKey(ctx, c, configPath, client, l, req); err != nil {
		t.Fatal(err)
	}
	if _, err = confirmRecoveryKey(ctx, c, configPath, client, l, req); err != nil {
		t.Fatal(err)
	}
	key, err := readRecoveryKey(req.File)
	if err != nil {
		t.Fatal(err)
	}
	stage := filepath.Join(l.Root, "staging", "damaged")
	if err = l.Snapshot(ctx, stage); err != nil {
		t.Fatal(err)
	}
	pinned, err := library.Open(stage)
	if err != nil {
		t.Fatal(err)
	}
	record, err := recoveryKeyRecordFor(pinned, key)
	if err != nil {
		t.Fatal(err)
	}
	record.WrappedVault[len(record.WrappedVault)-1] ^= 1
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err = pinned.SetSetting("recovery-key-"+key.ID, data); err != nil {
		t.Fatal(err)
	}
	if err = pinned.Verify(ctx); err != nil {
		t.Fatal("fixture must pass ciphertext-only verification", err)
	}
	if err = pinned.Close(); err != nil {
		t.Fatal(err)
	}
	id, err := client.BackupLibrary(ctx, stage)
	if err != nil {
		t.Fatal(err)
	}
	if err = client.Check(ctx); err != nil {
		t.Fatal("fixture must be restic-valid", err)
	}
	request := recoveryRequest{Source: recoverySource{Target: "local", Repository: c.BackupRepository, RecoveryKeyFile: req.File}, Snapshot: id, Directory: filepath.Join(base, "restored")}
	input, _ := json.Marshal(request)
	if err = recoveryManager(ctx, "recovery-restore", configPath, binary, bytes.NewReader(input)); err == nil {
		t.Error("unreadable private recovery wrapper reported a successful restore")
	} else {
		t.Log("restore rejected:", err)
	}
	if _, err = os.Stat(filepath.Join(request.Directory, recoveryReceipt)); !os.IsNotExist(err) {
		t.Error("unreadable private data received a successful restore receipt")
	}
	if _, err = os.Stat(filepath.Join(request.Directory, "index.db")); err != nil {
		t.Error("failed restore did not preserve files for diagnosis", err)
	}

	// The wrapper can also be valid while private plaintext metadata disagrees
	// with its original. Ciphertext hashes alone do not catch this failure.
	pinned, err = library.Open(stage)
	if err != nil {
		t.Fatal(err)
	}
	record.WrappedVault[len(record.WrappedVault)-1] ^= 1
	data, _ = json.Marshal(record)
	if err = pinned.SetSetting("recovery-key-"+key.ID, data); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite3", filepath.Join(stage, "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	var payload []byte
	if err = db.QueryRow("SELECT payload FROM private_items WHERE id=?", private.ID).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	plaintext, err := vault.Decrypt(payload, identity, 16384)
	if err != nil {
		t.Fatal(err)
	}
	var envelope map[string]json.RawMessage
	if err = json.Unmarshal(plaintext, &envelope); err != nil {
		t.Fatal(err)
	}
	var item map[string]any
	if err = json.Unmarshal(envelope["item"], &item); err != nil {
		t.Fatal(err)
	}
	item["size"] = 99
	envelope["item"], _ = json.Marshal(item)
	plaintext, _ = json.Marshal(envelope)
	var encrypted bytes.Buffer
	if err = vault.Encrypt(&encrypted, bytes.NewReader(plaintext), identity.Recipient()); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("UPDATE private_items SET payload=? WHERE id=?", encrypted.Bytes(), private.ID); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	if err = pinned.Verify(ctx); err != nil {
		t.Fatal("fixture must pass ciphertext checks", err)
	}
	if err = pinned.Close(); err != nil {
		t.Fatal(err)
	}
	id, err = client.BackupLibrary(ctx, stage)
	if err != nil {
		t.Fatal(err)
	}
	request.Snapshot, request.Directory = id, filepath.Join(base, "restored-content")
	input, _ = json.Marshal(request)
	if err = recoveryManager(ctx, "recovery-restore", configPath, binary, bytes.NewReader(input)); err == nil {
		t.Error("invalid private original reported success")
	}
	if _, err = os.Stat(filepath.Join(request.Directory, recoveryReceipt)); !os.IsNotExist(err) {
		t.Error("invalid private original received success receipt")
	}

	// Keeping the recovery secret cannot compensate for missing repository data.
	if err = os.Rename(filepath.Join(client.Repository, "data"), filepath.Join(base, "unavailable-packs")); err != nil {
		t.Fatal(err)
	}
	if err = client.Check(ctx); err == nil {
		t.Fatal("missing packs passed full check")
	}
	request.Directory = filepath.Join(base, "restored-missing-packs")
	input, _ = json.Marshal(request)
	if err = recoveryManager(ctx, "recovery-restore", configPath, binary, bytes.NewReader(input)); err == nil {
		t.Error("key incorrectly compensated for missing backup data")
	}
	if _, err = os.Stat(filepath.Join(request.Directory, recoveryReceipt)); !os.IsNotExist(err) {
		t.Error("missing packs received success receipt")
	}
}
