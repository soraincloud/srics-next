package main

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/soraincloud/srics-next/internal/library"
	"github.com/soraincloud/srics-next/internal/vault"
	"golang.org/x/crypto/bcrypt"
)

const syntheticPassword = "synthetic-local-password"

func TestManagerReportsDamagedLibraryWithoutRequestingNewPassword(t *testing.T) {
	base := t.TempDir()
	path := filepath.Join(base, "config.json")
	status, err := readManagerStatus(context.Background(), path)
	if err != nil || status.DataError != "" {
		t.Fatal("fresh installation reported data damage", err)
	}
	c := localConfig{Data: filepath.Join(base, "library"), Port: 19473}
	if err := applyConfig(path, configureRequest{Config: c, Password: syntheticPassword}); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(filepath.Join(c.Data, "index.db"), 0); err != nil {
		t.Fatal(err)
	}
	status, err = readManagerStatus(context.Background(), path)
	if err != nil || !strings.Contains(status.DataError, "截断") {
		t.Fatal("manager hid database damage", status.DataError, err)
	}
	if runtime.GOOS == "darwin" {
		if err = startManaged(context.Background(), path); err == nil || !strings.Contains(err.Error(), "截断") {
			t.Fatal("start requested credentials instead of reporting database damage", err)
		}
	}
}

func TestFailedCredentialChangePreservesBothPasswords(t *testing.T) {
	base := t.TempDir()
	path := filepath.Join(base, "config.json")
	c := localConfig{Data: filepath.Join(base, "library"), Port: 19473}
	req := configureRequest{Config: c, Password: syntheticPassword, VaultPassword: "synthetic-original-vault-password", VaultIdleMinutes: 7}
	if err := applyConfig(path, req); err != nil {
		t.Fatal(err)
	}
	l, err := library.Open(c.Data)
	if err != nil {
		t.Fatal(err)
	}
	oldWrapped, err := l.Setting("vault-key")
	if err != nil {
		t.Fatal(err)
	}
	l.Close()
	// Inject a write failure at the login-password update, after the old code
	// had already published the new vault wrapper and idle setting.
	db, err := sql.Open("sqlite3", filepath.Join(c.Data, "index.db")+"?mode=rw")
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TRIGGER reject_password BEFORE UPDATE ON settings WHEN NEW.key='password' BEGIN SELECT RAISE(ABORT, 'synthetic write failure'); END`)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	req.CurrentPassword, req.CurrentVaultPassword = req.Password, req.VaultPassword
	req.Password, req.VaultPassword, req.VaultIdleMinutes = "synthetic-new-login-password", "synthetic-new-vault-password", 12
	if err := applyConfig(path, req); err == nil {
		t.Fatal("injected failure did not abort configuration")
	}
	l, err = library.Open(c.Data)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	hash, err := l.Setting("password")
	if err != nil || bcrypt.CompareHashAndPassword(hash, []byte(syntheticPassword)) != nil {
		t.Fatal("failed save changed login password", err)
	}
	wrapped, err := l.Setting("vault-key")
	if err != nil || !bytes.Equal(wrapped, oldWrapped) {
		t.Fatal("failed save changed vault password", err)
	}
	idle, err := l.Setting("vault-idle")
	if err != nil || string(idle) != "7" {
		t.Fatal("failed save changed idle setting", err)
	}
}

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

func TestNetworkAndScheduleConfiguration(t *testing.T) {
	root := t.TempDir()
	c := localConfig{Data: filepath.Join(root, "library"), Port: 19473}
	for _, address := range []string{"0.0.0.0", "8.8.8.8", "example.com", "192.168.1.2:19473", "::", "127.0.0.1"} {
		c.LANAddress = address
		if c.validate() == nil {
			t.Fatal("invalid LAN address accepted", address)
		}
	}
	c.LANAddress = "192.168.1.2"
	if c.validate() != nil || c.url() != "https://192.168.1.2:19473" {
		t.Fatal("valid LAN address rejected")
	}
	c.BackupDailyAt = "03:00"
	if c.validate() == nil {
		t.Fatal("schedule without repository accepted")
	}
	c.BackupRepository, c.BackupPasswordFile = filepath.Join(root, "backup"), filepath.Join(root, "password")
	for _, at := range []string{"25:00", "03:61", "3:00", "03:00:00"} {
		c.BackupDailyAt = at
		if c.validate() == nil {
			t.Fatal("invalid schedule accepted", at)
		}
	}
	c.BackupDailyAt = "03:00"
	if err := c.validate(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(c.BackupPasswordFile, []byte("synthetic-backup-password"), 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "config.json")
	if err := applyConfig(path, configureRequest{Config: c, Password: syntheticPassword}); err != nil {
		t.Fatal(err)
	}
	status, err := readManagerStatus(context.Background(), path)
	if err != nil || !status.PasswordSet || status.Certificate == "" || status.CertFingerprint == "" {
		t.Fatal("missing certificate configuration", err)
	}
	if err := os.Remove(status.Certificate); err != nil {
		t.Fatal(err)
	}
	status, err = readManagerStatus(context.Background(), path)
	if err != nil || status.NetworkError == "" || !status.PasswordSet {
		t.Fatal("certificate loss prevents configuration recovery", err)
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
