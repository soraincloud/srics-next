package main

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/soraincloud/srics-next/internal/library"
	"github.com/soraincloud/srics-next/internal/recoverykey"
	"golang.org/x/crypto/bcrypt"
	"os"
	"path/filepath"
	"testing"
)

func TestUnifiedRecoveryCoversFutureVaultAndPasswordChanges(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	path := filepath.Join(base, "machine", "config.json")
	c := localConfig{Data: filepath.Join(base, "library"), Port: 19473}
	if err := applyConfig(path, configureRequest{Config: c, Password: syntheticPassword}); err != nil {
		t.Fatal(err)
	}
	l, err := library.Open(c.Data)
	if err != nil {
		t.Fatal(err)
	}
	req := recoveryKeyRequest{File: filepath.Join(base, "offline.json")}
	if _, err = unifiedKey(ctx, "unified-key-generate", path, l, c, req); err != nil {
		t.Fatal(err)
	}
	key, err := readRecoveryKey(req.File)
	if err != nil {
		t.Fatal(err)
	}
	original, _ := os.ReadFile(req.File)
	if _, err = unifiedKey(ctx, "unified-key-confirm", path, l, c, req); err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = unifiedKey(cancelled, "unified-key-confirm", path, l, c, req); err == nil {
		t.Fatal("cancelled confirm succeeded")
	}
	l.Close()
	// No JSON is supplied when first enabling a vault or changing both passwords.
	configure := configureRequest{Config: c, VaultPassword: "synthetic-new-vault-pass"}
	if err = applyConfig(path, configure); err != nil {
		t.Fatal(err)
	}
	l, err = library.Open(c.Data)
	if err != nil {
		t.Fatal(err)
	}
	a, err := recoveryKeyAccess(ctx, l, req.File)
	if err != nil || a == nil {
		t.Fatal("future vault not recoverable", err)
	}
	items, err := l.ReceivePrivate(ctx, a, library.NewID(), "files", "future.bin", 6, bytes.NewBufferString("secret"))
	if err != nil {
		t.Fatal(err)
	}
	a.Lock()
	l.Close()
	configure.Password = "synthetic-changed-login-pass"
	configure.CurrentPassword = syntheticPassword
	configure.CurrentVaultPassword = configure.VaultPassword
	configure.VaultPassword = "synthetic-changed-vault-pass"
	if err = applyConfig(path, configure); err != nil {
		t.Fatal(err)
	}
	l, err = library.Open(c.Data)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	a, err = recoveryKeyAccess(ctx, l, req.File)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Lock()
	if err = verifyPrivateContents(ctx, l, a); err != nil {
		t.Fatal("password change broke originals", err)
	}
	all, err := l.PrivateItems(ctx, a)
	if err != nil || len(all) != 1 || all[0].ID != items.ID {
		t.Fatal(err)
	}
	hash, _ := l.Setting("password")
	if bcrypt.CompareHashAndPassword(hash, []byte(configure.Password)) != nil {
		t.Fatal("login password failed")
	}
	// Recovery JSON copied from an external filesystem remains usable.
	copied := filepath.Join(base, "usb-copy.json")
	if err = os.WriteFile(copied, original, 0644); err != nil {
		t.Fatal(err)
	}
	copiedKey, err := readRecoveryKey(copied)
	if err != nil || copiedKey.ID != key.ID {
		t.Fatal("offline copy rejected", err)
	}
	after, _ := os.ReadFile(req.File)
	if !bytes.Equal(original, after) {
		t.Fatal("offline JSON changed")
	}
	record, _ := l.Setting(recoverykey.Setting)
	if bytes.Contains(record, []byte(key.Secret)) || bytes.Contains(record, []byte(configure.VaultPassword)) {
		t.Fatal("emergency secret persisted")
	}
	snapshot := filepath.Join(c.Data, "staging", "test")
	if err = l.Snapshot(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	manifest, _ := os.ReadFile(filepath.Join(snapshot, "recovery-keys.json"))
	var ids []string
	if json.Unmarshal(manifest, &ids) != nil || len(ids) != 1 || ids[0] != key.ID {
		t.Fatal("future snapshot missing root key")
	}
	// A corrupted public recovery record must fail before publishing snapshots.
	var r recoverykey.Record
	if json.Unmarshal(record, &r) != nil {
		t.Fatal("record")
	}
	r.Recipient = "invalid"
	bad, _ := json.Marshal(r)
	if err = l.SetSetting(recoverykey.Setting, bad); err != nil {
		t.Fatal(err)
	}
	if err = l.Snapshot(ctx, filepath.Join(c.Data, "staging", "bad")); err == nil {
		t.Fatal("malformed emergency record accepted")
	}
}
