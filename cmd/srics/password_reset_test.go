package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/soraincloud/srics-next/internal/library"
	"github.com/soraincloud/srics-next/internal/vault"
	"golang.org/x/crypto/bcrypt"
)

func TestLocalPasswordResetPreservesPrivateDataAndRecovery(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	path := filepath.Join(root, "config.json")
	c := localConfig{Data: filepath.Join(root, "library"), Port: 19473}
	if err := applyConfig(path, configureRequest{Config: c, Password: "old-login-password", VaultPassword: "old-vault-password"}); err != nil {
		t.Fatal(err)
	}
	l, err := library.Open(c.Data)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	oldHash, _ := l.Setting("password")
	oldWrapped, _ := l.Setting("vault-key")
	identity, err := vault.Unlock(oldWrapped, "old-vault-password")
	if err != nil {
		t.Fatal(err)
	}
	a := vault.NewAccess(ctx, identity, time.Hour)
	defer a.Lock()
	original := []byte("synthetic private original survives independent password management")
	item, err := l.ReceivePrivate(ctx, a, library.NewID(), "files", "secret.txt", int64(len(original)), bytes.NewReader(original))
	if err != nil {
		t.Fatal(err)
	}
	cipherBefore, err := os.ReadFile(l.PrivatePath(item.Object))
	if err != nil {
		t.Fatal(err)
	}
	key := recoveryKeyFile{Format: "srics-recovery-key", Version: 1, RepositoryID: strings.Repeat("a", 64), LibraryID: library.NewID(), CreatedAt: time.Now(), Secret: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))}
	key.ID = digest([]byte(key.Secret))
	recoveryWrapped, err := vault.Wrap(identity, key.Secret)
	if err != nil {
		t.Fatal(err)
	}
	record := recoveryKeyRecord{Info: recoveryKeyInfo{ID: key.ID, RepositoryID: key.RepositoryID, State: "verified", VaultPresent: true}, WrappedVault: recoveryWrapped, VaultDigest: digest(oldWrapped)}
	recordJSON, _ := json.Marshal(record)
	if err := l.SetSettings(map[string][]byte{"recovery-library-id": []byte(key.LibraryID), "recovery-key-" + key.ID: recordJSON}); err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(root, "recovery.json")
	keyJSON, _ := json.Marshal(key)
	if err := os.WriteFile(keyPath, keyJSON, 0600); err != nil {
		t.Fatal(err)
	}
	// The daily reset endpoint accepts login passwords only. Old clients cannot
	// use recovery JSON or vault-password fields to alter a live library.
	for _, request := range []string{
		`{"password":"must-not-be-saved","recoveryKeyFile":"` + keyPath + `"}`,
		`{"password":"must-not-be-saved","vaultPassword":"must-not-be-saved"}`,
	} {
		if err := resetLocalPasswordsManager(ctx, path, strings.NewReader(request)); err == nil {
			t.Fatal("daily reset accepted backup recovery or vault fields")
		}
	}
	currentHash, _ := l.Setting("password")
	currentWrapped, _ := l.Setting("vault-key")
	if !bytes.Equal(currentHash, oldHash) || !bytes.Equal(currentWrapped, oldWrapped) {
		t.Fatal("rejected reset changed credentials")
	}
	newLogin := " 新-login-cafe\u0301-🔑-password "
	if err := resetLocalPasswords(ctx, l, localPasswordReset{Password: newLogin}); err != nil {
		t.Fatal(err)
	}
	currentHash, _ = l.Setting("password")
	currentWrapped, _ = l.Setting("vault-key")
	if bcrypt.CompareHashAndPassword(currentHash, []byte(newLogin)) != nil || !bytes.Equal(currentWrapped, oldWrapped) {
		t.Fatal("login reset failed or altered vault")
	}
	if err = l.Close(); err != nil {
		t.Fatal(err)
	}
	// Normal vault changes require the current vault password, never JSON.
	for _, wrong := range []string{"", "wrong-vault-password", key.Secret} {
		err = applyConfig(path, configureRequest{Config: c, VaultPassword: "new-vault-password", CurrentVaultPassword: wrong})
		if err == nil {
			t.Fatal("vault change accepted without old vault password")
		}
	}
	l, err = library.Open(c.Data)
	if err != nil {
		t.Fatal(err)
	}
	currentWrapped, _ = l.Setting("vault-key")
	if !bytes.Equal(currentWrapped, oldWrapped) {
		t.Fatal("failed vault change altered wrapping")
	}
	l.Close()
	if err = applyConfig(path, configureRequest{Config: c, VaultPassword: "new-vault-password", CurrentVaultPassword: "old-vault-password"}); err != nil {
		t.Fatal(err)
	}
	l, err = library.Open(c.Data)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	currentWrapped, _ = l.Setting("vault-key")
	newIdentity, err := vault.Unlock(currentWrapped, "new-vault-password")
	if err != nil {
		t.Fatal(err)
	}
	if newIdentity.String() != identity.String() {
		t.Fatal("vault identity replaced")
	}
	if _, err := vault.Unlock(currentWrapped, "old-vault-password"); err == nil {
		t.Fatal("old vault password accepted")
	}
	newAccess := vault.NewAccess(ctx, newIdentity, time.Hour)
	defer newAccess.Lock()
	items, err := l.PrivateItems(ctx, newAccess)
	if err != nil || len(items) != 1 {
		t.Fatal("private index lost", err)
	}
	reader, _, err := l.PrivateRead(ctx, newAccess, items[0], false)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := io.ReadAll(reader)
	reader.Close()
	if err != nil || !bytes.Equal(restored, original) {
		t.Fatal("private original changed", err)
	}
	cipherAfter, _ := os.ReadFile(l.PrivatePath(item.Object))
	if !bytes.Equal(cipherBefore, cipherAfter) {
		t.Fatal("ciphertext was rewritten")
	}
	// Password management must preserve emergency backup recovery material.
	recovery, err := recoveryKeyAccess(ctx, l, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	defer recovery.Lock()
	if err := verifyPrivateContents(ctx, l, recovery); err != nil {
		t.Fatal("recovery material no longer decrypts data", err)
	}
	recordAfter, _ := l.Setting("recovery-key-" + key.ID)
	keyAfter, _ := os.ReadFile(keyPath)
	if !bytes.Equal(recordAfter, recordJSON) || !bytes.Equal(keyAfter, keyJSON) {
		t.Fatal("password change altered recovery record or JSON")
	}
	hashAfter, _ := l.Setting("password")
	if !bytes.Equal(hashAfter, currentHash) {
		t.Fatal("vault change altered login")
	}
}
func TestLocalPasswordResetValidation(t *testing.T) {
	for _, password := range []string{"", "short", "password-with-newline\n", strings.Repeat("x", 73)} {
		var issue *fieldError
		if err := (localPasswordReset{Password: password}).validate(); !errors.As(err, &issue) || issue.Field != "password" {
			t.Fatal("wrong validation field", err)
		}
	}
	if err := resetLocalPasswordsManager(context.Background(), filepath.Join(t.TempDir(), "missing.json"), strings.NewReader(`{"password":"synthetic-password"}`)); err == nil {
		t.Fatal("reset initialized missing library")
	}
}
