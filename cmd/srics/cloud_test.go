package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/soraincloud/srics-next/internal/backup"
)

func TestCloudSecretsAndLocalConfigurationCompatibility(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "config.json")
	if err := os.WriteFile(configPath, []byte(`{"data":"`+filepath.Join(root, "library")+`","port":19473}`), 0600); err != nil {
		t.Fatal(err)
	}
	c, _, err := loadConfig(configPath)
	if err != nil || c.Cloud.Enabled {
		t.Fatal("old config incompatible", err)
	}
	c.Cloud = cloudConfig{Enabled: true, Connection: backup.S3Config{Endpoint: "https://storage.example.com", Region: "us-east-1", Bucket: "srics-test", Prefix: "srics/main"}, CredentialsFile: filepath.Join(root, "cloud.json"), PasswordFile: filepath.Join(root, "password")}
	c.BackupDailyAt = "03:00"
	secret := `{"accessKeyId":"synthetic-key","secretAccessKey":"synthetic-secret"}`
	if err = os.WriteFile(c.Cloud.CredentialsFile, []byte(secret), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(c.Cloud.PasswordFile, []byte("synthetic-cloud-password"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = c.validate(); err != nil {
		t.Fatal("cloud-only daily schedule rejected", err)
	}
	client, err := configuredCloud(c, "restic")
	if err != nil || client.S3 == nil || client.Repository != "s3:https://storage.example.com/srics-test/srics/main" {
		t.Fatal("cloud client", err)
	}
	if err = writeConfig(configPath, c); err != nil {
		t.Fatal(err)
	}
	saved, _ := os.ReadFile(configPath)
	if strings.Contains(string(saved), "synthetic-") {
		t.Fatal("credential contents saved in config")
	}
	if err = os.Chmod(c.Cloud.CredentialsFile, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err = configuredCloud(c, "restic"); err == nil {
		t.Fatal("shared credentials accepted")
	}
	if err = os.Chmod(c.Cloud.CredentialsFile, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(c.Data, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err = os.Symlink(c.Data, alias); err != nil {
		t.Fatal(err)
	}
	bad := c
	bad.Cloud.PasswordFile = c.Cloud.CredentialsFile
	if bad.validate() == nil {
		t.Fatal("cloud credential used as backup passphrase")
	}
	bad = c
	bad.Cloud.CredentialsFile = filepath.Join(alias, "secret.json")
	if bad.validate() == nil {
		t.Fatal("credentials inside library via symlink accepted")
	}
	bad = c
	bad.BackupRepository = root
	bad.BackupPasswordFile = c.Cloud.PasswordFile
	if validateCloud(bad) == nil {
		t.Fatal("credentials inside local backup accepted")
	}
}
