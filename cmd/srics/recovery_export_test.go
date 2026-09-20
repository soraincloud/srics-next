package main

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/soraincloud/srics-next/internal/library"
	"github.com/soraincloud/srics-next/internal/vault"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSingleItemRecoveryRequiresUnlockAndDoesNotSwitch(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	data := filepath.Join(root, "restored")
	if err := library.Create(data); err != nil {
		t.Fatal(err)
	}
	l, err := library.Open(data)
	if err != nil {
		t.Fatal(err)
	}
	novel, err := l.CreateNovel(library.NewID(), "单项取回", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = l.CreateChapter(novel.ID, library.NewID(), "一", "单项正文", novel.Revision); err != nil {
		t.Fatal(err)
	}
	wrapped, err := l.PrepareVault("synthetic-export-password", "")
	if err != nil {
		t.Fatal(err)
	}
	if err = l.SetSetting("vault-key", wrapped); err != nil {
		t.Fatal(err)
	}
	key, err := vault.Unlock(wrapped, "synthetic-export-password")
	if err != nil {
		t.Fatal(err)
	}
	a := vault.NewAccess(ctx, key, time.Minute)
	private, err := l.ReceivePrivate(ctx, a, library.NewID(), "files", "secret.txt", 6, bytes.NewReader([]byte("secret")))
	if err != nil {
		t.Fatal(err)
	}
	a.Lock()
	l.Close()
	record := recoveryResult{Version: 1, Snapshot: strings.Repeat("a", 64), Directory: data, VerifiedAt: time.Now()}
	b, _ := json.Marshal(record)
	os.WriteFile(filepath.Join(data, recoveryReceipt), b, 0600)
	path := filepath.Join(root, "config", "config.json")
	os.Mkdir(filepath.Dir(path), 0700)
	if err = writeConfig(path, localConfig{Data: filepath.Join(root, "live"), Port: 19473}); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	req := recoveryRequest{Directory: data}
	out, err := recoveryExportRequest(ctx, path, req)
	if err != nil || len(out.Items) != 1 {
		t.Fatal(out, err)
	}
	req.ItemID = private.ID
	req.Private = true
	req.ExportDirectory = root
	if _, err = recoveryExportRequest(ctx, path, req); err == nil {
		t.Fatal("locked export succeeded")
	}
	req.VaultPassword = "synthetic-export-password"
	out, err = recoveryExportRequest(ctx, path, req)
	if err != nil {
		t.Fatal(err)
	}
	b, err = os.ReadFile(out.Exported)
	if err != nil || string(b) != "secret" {
		t.Fatal(string(b), err)
	}
	info, _ := os.Stat(out.Exported)
	if info.Mode().Perm() != 0600 {
		t.Fatal(info.Mode())
	}
	req.Private = false
	req.ItemID = novel.ID
	out, err = recoveryExportRequest(ctx, path, req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(out.Exported)
	if !bytes.Contains(b, []byte("单项正文")) {
		t.Fatal(string(b))
	}
	req.ExportDirectory = data
	if _, err = recoveryExportRequest(ctx, path, req); err == nil {
		t.Fatal("export into restored library allowed")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("export switched active library")
	}
}
