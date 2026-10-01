package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/soraincloud/srics-next/internal/library"
)

func TestBackupSetupRejectsUnsafeTargetsAndSecretReplacement(t *testing.T) {
	root := t.TempDir()
	config := filepath.Join(root, "config.json")
	c := localConfig{Data: filepath.Join(root, "library"), Port: 19473}
	if err := library.Create(c.Data); err != nil {
		t.Fatal(err)
	}
	l, err := library.Open(c.Data)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if err = writeConfig(config, c); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(config)
	for _, repo := range []string{filepath.Join(c.Data, "backup"), root, "relative/path"} {
		if _, err = prepareBackupSetup(context.Background(), config, "missing-restic", c, l, backupSetupRequest{Target: "local", Mode: "new", Repository: repo}); err == nil {
			t.Fatalf("unsafe target accepted: %s", repo)
		}
	}
	after, _ := os.ReadFile(config)
	if !bytes.Equal(before, after) {
		t.Fatal("invalid setup changed configuration")
	}
	key := filepath.Join(root, "existing.key")
	original := []byte("preserve-existing-secret")
	if err = setupSecret(key, original); err != nil {
		t.Fatal(err)
	}
	if err = setupSecret(key, []byte("replacement")); err == nil {
		t.Fatal("key was replaced")
	}
	saved, _ := os.ReadFile(key)
	if !bytes.Equal(saved, original) {
		t.Fatal("key changed")
	}
	alias := filepath.Join(root, "alias")
	if err = os.Symlink(c.Data, alias); err != nil {
		t.Fatal(err)
	}
	if err = setupDirectory(alias, c); err == nil {
		t.Fatal("symlink secret directory accepted")
	}
}
