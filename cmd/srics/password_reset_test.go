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
	original := []byte("synthetic private original survives resetting both forgotten passwords")
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
	// A wrong library key must not partially reset the login password.
	key.LibraryID = library.NewID()
	wrongJSON, _ := json.Marshal(key)
	wrongPath := filepath.Join(root, "wrong.json")
	if err := os.WriteFile(wrongPath, wrongJSON, 0600); err != nil {
		t.Fatal(err)
	}
	if err := resetLocalPasswords(ctx, l, localPasswordReset{Password: "new-login-password", VaultPassword: "new-vault-password", RecoveryKeyFile: wrongPath}); err == nil {
		t.Fatal("accepted foreign key")
	}
	currentHash, _ := l.Setting("password")
	currentWrapped, _ := l.Setting("vault-key")
	if !bytes.Equal(currentHash, oldHash) || !bytes.Equal(currentWrapped, oldWrapped) {
		t.Fatal("failed reset changed credentials")
	}
	// Login-only reset never needs the vault credential or changes its wrapping.
	if err := resetLocalPasswords(ctx, l, localPasswordReset{Password: "new-login-password"}); err != nil {
		t.Fatal(err)
	}
	currentWrapped, _ = l.Setting("vault-key")
	if !bytes.Equal(currentWrapped, oldWrapped) {
		t.Fatal("login reset changed vault")
	}
	// Reset both with the matching offline JSON, without either old password.
	newLogin := " 新-login-cafe\u0301-🔑-password "
	if err := resetLocalPasswords(ctx, l, localPasswordReset{Password: newLogin, VaultPassword: "new-vault-password", RecoveryKeyFile: keyPath}); err != nil {
		t.Fatal(err)
	}
	currentHash, _ = l.Setting("password")
	if bcrypt.CompareHashAndPassword(currentHash, []byte(newLogin)) != nil || bcrypt.CompareHashAndPassword(currentHash, []byte("new-login-password")) == nil {
		t.Fatal("login reset failed")
	}
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
	recovery, err := recoveryKeyAccess(ctx, l, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	defer recovery.Lock()
	if err := verifyPrivateContents(ctx, l, recovery); err != nil {
		t.Fatal("original recovery key stopped working", err)
	}
	recordAfter, _ := l.Setting("recovery-key-" + key.ID)
	if !bytes.Equal(recordAfter, recordJSON) {
		t.Fatal("recovery record changed")
	}
	// Vault-only reset leaves login hash unchanged.
	if err := resetLocalPasswords(ctx, l, localPasswordReset{VaultPassword: "second-new-vault-password", RecoveryKeyFile: keyPath}); err != nil {
		t.Fatal(err)
	}
	hashAfter, _ := l.Setting("password")
	if !bytes.Equal(hashAfter, currentHash) {
		t.Fatal("vault-only reset changed login")
	}
}
func TestLocalPasswordResetValidation(t *testing.T) {
	for _, tc := range []struct {
		req   localPasswordReset
		field string
	}{
		{localPasswordReset{Password: "short"}, "password"},
		{localPasswordReset{Password: "password-with-newline\n"}, "password"},
		{localPasswordReset{VaultPassword: "short"}, "vaultPassword"},
		{localPasswordReset{VaultPassword: "long-enough-vault\r"}, "vaultPassword"},
		{localPasswordReset{VaultPassword: "long-enough-vault"}, "recoveryKeyFile"},
	} {
		var issue *fieldError
		if err := tc.req.validate(); !errors.As(err, &issue) || issue.Field != tc.field {
			t.Fatal("wrong validation field", err)
		}
	}
	if (localPasswordReset{}).validate() == nil {
		t.Fatal("accepted empty request")
	}
	if err := resetLocalPasswordsManager(context.Background(), filepath.Join(t.TempDir(), "missing.json"), strings.NewReader(`{"password":"synthetic-password"}`)); err == nil {
		t.Fatal("reset initialized missing library")
	}
}
