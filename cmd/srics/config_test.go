package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/soraincloud/srics-next/internal/library"
	"github.com/soraincloud/srics-next/internal/vault"
	"golang.org/x/crypto/bcrypt"
)

const syntheticPassword = "synthetic-local-password"

func TestLocalPasswordConfigurationAndChange(t *testing.T) {
	base := t.TempDir()
	path := filepath.Join(base, "config.json")
	c := localConfig{Data: filepath.Join(base, "library"), Port: 19473}
	if err := applyConfig(path, configureRequest{Config: c, Password: "short"}); err == nil {
		t.Fatal("weak password accepted")
	}
	if _, err := os.Stat(c.Data); !os.IsNotExist(err) {
		t.Fatal("invalid request created a library")
	}
	if err := applyConfig(path, configureRequest{Config: c, Password: syntheticPassword}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if strings.Contains(string(b), syntheticPassword) || strings.Contains(string(b), "password\"") {
		t.Fatal("login credential in config")
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatal("configuration permissions", info.Mode())
	}
	checkPassword := func(password string) {
		t.Helper()
		l, err := library.Open(c.Data)
		if err != nil {
			t.Fatal(err)
		}
		defer l.Close()
		hash, err := l.Setting("password")
		if err != nil {
			t.Fatal(err)
		}
		if bcrypt.CompareHashAndPassword(hash, []byte(password)) != nil {
			t.Fatal("password changed unexpectedly")
		}
	}
	checkPassword(syntheticPassword)
	changed := c
	changed.Port++
	if err := applyConfig(path, configureRequest{Config: changed, Password: "replacement-local-password", CurrentPassword: "incorrect"}); err == nil {
		t.Fatal("changed password without old credential")
	}
	loaded, _, _ := loadConfig(path)
	if loaded.Port != c.Port {
		t.Fatal("failed password change modified config")
	}
	if err := applyConfig(path, configureRequest{Config: changed}); err != nil {
		t.Fatal(err)
	}
	checkPassword(syntheticPassword)
	if err := applyConfig(path, configureRequest{Config: changed, Password: "replacement-local-password", CurrentPassword: syntheticPassword}); err != nil {
		t.Fatal(err)
	}
	checkPassword("replacement-local-password")
	l, err := library.Open(c.Data)
	if err != nil {
		t.Fatal(err)
	}
	if err := applyConfig(path, configureRequest{Config: changed}); err == nil {
		t.Fatal("configured a locked library")
	}
	l.Close()
	if err := os.Rename(c.Data, c.Data+"-unmounted"); err != nil {
		t.Fatal(err)
	}
	if err := applyConfig(path, configureRequest{Config: changed}); err == nil {
		t.Fatal("recreated missing data")
	}
	if _, err := os.Stat(c.Data); !os.IsNotExist(err) {
		t.Fatal("missing data was recreated")
	}
}
func TestConfigPreservesLegacyLibraryAndMissingDiskMarker(t *testing.T) {
	base := t.TempDir()
	path := filepath.Join(base, "config.json")
	c := localConfig{Data: filepath.Join(base, "library"), Port: 19473}
	if err := os.WriteFile(filepath.Join(base, "initialized"), []byte("srics-library-v1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := applyConfig(path, configureRequest{Config: c, Password: syntheticPassword}); err == nil {
		t.Fatal("ignored missing disk marker")
	}
	if err := library.Create(c.Data); err != nil {
		t.Fatal(err)
	}
	l, _ := library.Open(c.Data)
	hash, _ := bcrypt.GenerateFromPassword([]byte(syntheticPassword), bcrypt.MinCost)
	if err := l.Setup(hash); err != nil {
		t.Fatal(err)
	}
	l.Close()
	if err := applyConfig(path, configureRequest{Config: c}); err != nil {
		t.Fatal(err)
	}
	other := c
	other.Data = filepath.Join(base, "different")
	if err := applyConfig(path, configureRequest{Config: other}); err == nil {
		t.Fatal("silently changed existing data path")
	}
}
func TestBackupConfigurationBoundaries(t *testing.T) {
	base := t.TempDir()
	secret := filepath.Join(base, "password.txt")
	if err := os.WriteFile(secret, []byte("synthetic-backup-password\n"), 0600); err != nil {
		t.Fatal(err)
	}
	c := localConfig{Data: filepath.Join(base, "library"), Port: 19473, BackupRepository: filepath.Join(base, "backup"), BackupPasswordFile: secret}
	if _, err := configuredBackup(c, "restic"); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(secret, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := configuredBackup(c, "restic"); err == nil {
		t.Fatal("accepted shared password file")
	}
	for _, target := range []string{c.Data, filepath.Join(c.Data, "backup"), base} {
		bad := c
		bad.BackupRepository = target
		if bad.validate() == nil {
			t.Fatal("accepted overlap", target)
		}
	}
	if err := os.Mkdir(c.Data, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(base, "alias")
	if err := os.Symlink(c.Data, alias); err != nil {
		t.Fatal(err)
	}
	c.BackupRepository = filepath.Join(alias, "backup")
	if c.validate() == nil {
		t.Fatal("accepted symlink overlap")
	}
}
func TestLaunchPlistEscapesPathsAndContainsNoCredentials(t *testing.T) {
	plist := launchPlist("/tmp/a&b/config.json", "/tmp/<app>/srics", managerStatus{Log: "/tmp/a&b/log"})
	for _, expected := range []string{"a&amp;b", "&lt;app&gt;", "<string>--config</string>"} {
		if !strings.Contains(plist, expected) {
			t.Fatal("invalid launch configuration", expected)
		}
	}
	if strings.Contains(plist, "Password") || strings.Contains(plist, "KeepAlive") {
		t.Fatal("unexpected launch persistence or credentials")
	}
}

func TestLocalVaultPassphraseAndRotation(t *testing.T) {
	base := t.TempDir()
	path := filepath.Join(base, "config.json")
	c := localConfig{Data: filepath.Join(base, "library"), Port: 19473}
	req := configureRequest{Config: c, Password: syntheticPassword, VaultPassword: "synthetic-vault-original", VaultIdleMinutes: 7}
	if err := applyConfig(path, req); err != nil {
		t.Fatal(err)
	}
	l, err := library.Open(c.Data)
	if err != nil {
		t.Fatal(err)
	}
	wrapped, _ := l.Setting("vault-key")
	idle, _ := l.Setting("vault-idle")
	l.Close()
	key, err := vault.Unlock(wrapped, req.VaultPassword)
	if err != nil || string(idle) != "7" {
		t.Fatal("vault setup failed", err)
	}
	req.Password = ""
	req.VaultPassword = "synthetic-vault-replacement"
	req.CurrentVaultPassword = "incorrect"
	req.Config.Port++
	if err = applyConfig(path, req); err == nil {
		t.Fatal("vault changed without old passphrase")
	}
	saved, _, _ := loadConfig(path)
	if saved.Port != c.Port {
		t.Fatal("failed rotation changed config")
	}
	req.CurrentVaultPassword = "synthetic-vault-original"
	if err = applyConfig(path, req); err != nil {
		t.Fatal(err)
	}
	l, err = library.Open(c.Data)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	changed, _ := l.Setting("vault-key")
	if _, err = vault.Unlock(changed, req.CurrentVaultPassword); err == nil {
		t.Fatal("old passphrase still unlocks new wrapper")
	}
	newer, err := vault.Unlock(changed, req.VaultPassword)
	if err != nil || newer.String() != key.String() {
		t.Fatal("rotation changed data identity", err)
	}
	if _, err = vault.Unlock(wrapped, req.CurrentVaultPassword); err != nil {
		t.Fatal("historical wrapper lost", err)
	}
	config, _ := os.ReadFile(path)
	if strings.Contains(string(config), "synthetic-vault") {
		t.Fatal("plaintext passphrase in config")
	}
}
