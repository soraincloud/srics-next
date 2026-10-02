//go:build integration

package main

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/soraincloud/srics-next/internal/backup"
	"github.com/soraincloud/srics-next/internal/library"
	"github.com/soraincloud/srics-next/internal/media"
	"github.com/soraincloud/srics-next/internal/recoverykey"
	"github.com/soraincloud/srics-next/internal/vault"
	"golang.org/x/crypto/bcrypt"
)

func TestUnifiedPackageSurvivesTotalMachineLoss(t *testing.T) {
	binary, err := exec.LookPath("restic")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
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
	type original struct {
		module  string
		private bool
		pages   [][]byte
	}
	originals := map[string]original{}
	webp, err := os.ReadFile(filepath.Join("..", "..", "testdata", "media", "lossless-bare.webp"))
	if err != nil {
		t.Fatal(err)
	}
	lossy, err := os.ReadFile(filepath.Join("..", "..", "testdata", "media", "lossy-alpha.webp"))
	if err != nil {
		t.Fatal(err)
	}
	for _, module := range []string{"comics", "images", "photos"} {
		pages := [][]byte{webp}
		if module == "comics" {
			pages = append(pages, lossy)
		}
		up := library.Upload{ID: library.NewID(), Module: module, Name: "synthetic " + module}
		for i, p := range pages {
			up.Files = append(up.Files, library.UploadFile{Name: fmt.Sprintf("%05d.webp", i+1), Size: int64(len(p))})
		}
		if _, err = l.CreateUpload(up); err != nil {
			t.Fatal(err)
		}
		for i, p := range pages {
			if _, err = l.Receive(ctx, up.ID, i, fmt.Sprintf("%x", sha256.Sum256(p)), bytes.NewReader(p), media.Converter{}); err != nil {
				t.Fatal(err)
			}
		}
		if _, err = l.Finish(up.ID); err != nil {
			t.Fatal(err)
		}
		originals[up.ID] = original{module: module, pages: pages}
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
	if err != nil {
		t.Fatal(err)
	}
	originals[private.ID] = original{module: "files", private: true, pages: [][]byte{plain}}
	photo, err := l.ReceivePrivate(ctx, a, library.NewID(), "private", "private.webp", int64(len(webp)), bytes.NewReader(webp))
	if err != nil {
		t.Fatal(err)
	}
	originals[photo.ID] = original{module: "private", private: true, pages: [][]byte{webp}}
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
	if _, err = unifiedKey(ctx, "unified-key-generate", configPath, l, c, req); err == nil {
		t.Fatal("wrong vault password accepted")
	}
	if _, err = os.Stat(req.File); !os.IsNotExist(err) {
		t.Fatal("failed generation left secret file")
	}
	req.VaultPassword = "synthetic-original-vault-password"
	unsafe := req
	unsafe.File = filepath.Join(c.Data, "leaked-key.json")
	if _, err = unifiedKey(ctx, "unified-key-generate", configPath, l, c, unsafe); err == nil {
		t.Fatal("secret export into library allowed")
	}
	alias := filepath.Join(base, "alias")
	if err = os.Symlink(c.BackupRepository, alias); err != nil {
		t.Fatal(err)
	}
	unsafe.File = filepath.Join(alias, "leaked-key.json")
	if _, err = unifiedKey(ctx, "unified-key-generate", configPath, l, c, unsafe); err == nil {
		t.Fatal("symlink export into repository allowed")
	}
	out, err := unifiedKey(ctx, "unified-key-generate", configPath, l, c, req)
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
	if _, err = unifiedKey(ctx, "unified-key-generate", configPath, l, c, req); err == nil {
		t.Fatal("existing key overwritten")
	}
	fileAfter, _ := os.ReadFile(req.File)
	if !bytes.Equal(fileBefore, fileAfter) {
		t.Fatal("existing recovery file changed")
	}
	response, _ := json.Marshal(out)
	record, _ := l.Setting(recoverykey.Setting)
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
	if _, err = unifiedKey(cancelled, "unified-key-confirm", configPath, l, c, req); err == nil {
		t.Fatal("cancelled confirmation succeeded")
	}
	out, err = unifiedKey(ctx, "unified-key-confirm", configPath, l, c, req)
	if err != nil {
		t.Fatal(err)
	}
	if out.Info.State != "verified" || out.Info.VerifiedAt == nil {
		t.Fatal("key not verified")
	}
	_ = oldID
	if _, err = unifiedKey(ctx, "unified-key-generate", configPath, l, c, recoveryKeyRequest{File: filepath.Join(base, "second.json"), VaultPassword: req.VaultPassword}); err == nil {
		t.Fatal("verified key silently replaced")
	}
	cache := backup.UnifiedConfig{Repository: filepath.Join(machine, "cache", "repository"), PasswordFile: filepath.Join(machine, "cache", "daily-key"), Directory: filepath.Join(base, "packages")}
	if err = os.MkdirAll(filepath.Dir(cache.Repository), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(cache.Directory, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(cache.PasswordFile, []byte("synthetic-daily-cache-password"), 0600); err != nil {
		t.Fatal(err)
	}
	c.Unified = &cache
	plan := backup.Unified{Config: cache, Binary: binary}
	managed, e := plan.Client()
	if e != nil {
		t.Fatal(e)
	}
	if err = managed.Init(ctx); err != nil {
		t.Fatal(err)
	}
	if err = writeConfig(configPath, c); err != nil {
		t.Fatal(err)
	}
	first, e := plan.Run(ctx, l, "")
	if e != nil {
		t.Fatal(e)
	}
	firstManifest, e := backup.InspectPackage(first.File)
	if e != nil || firstManifest.Version != 2 || firstManifest.RecoveryKeyID != key.ID {
		t.Fatal("first format", e)
	}
	// Daily password rotation protects the same vault identity with the same
	// public recovery recipient. The exported offline JSON remains unchanged.
	newWrapped, err := l.PrepareVault("synthetic-changed-vault-password", req.VaultPassword)
	if err != nil {
		t.Fatal(err)
	}
	values, e := l.VaultRecoverySettings(newWrapped, "synthetic-changed-vault-password")
	if e != nil {
		t.Fatal(e)
	}
	values["vault-key"] = newWrapped
	if err = l.SetSettings(values); err != nil {
		t.Fatal(err)
	}
	a, err = recoveryKeyAccess(ctx, l, req.File)
	if err != nil {
		t.Fatal("rotation broke recovery", err)
	}
	a.Lock()
	var futureID string
	// Simulate replacing the internal cache and destination. Neither operation
	// changes the library's emergency JSON or requires its private secret.
	secondCache := cache
	secondCache.Repository = filepath.Join(machine, "replacement", "repository")
	secondCache.PasswordFile = filepath.Join(machine, "replacement", "daily-key")
	secondCache.Directory = filepath.Join(base, "second-packages")
	if err = os.MkdirAll(filepath.Dir(secondCache.Repository), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(secondCache.Directory, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(secondCache.PasswordFile, []byte("synthetic-other-cache-password"), 0600); err != nil {
		t.Fatal(err)
	}
	second := backup.Unified{Config: secondCache, Binary: binary}
	replacement, e := second.Client()
	if e != nil {
		t.Fatal(e)
	}
	if err = replacement.Init(ctx); err != nil {
		t.Fatal(err)
	}
	c.Unified = &secondCache
	plan = second
	// The only surviving backup is now the portable package, not a directory.
	packageDirectory := filepath.Join(base, "surviving-packages")
	if err = os.Mkdir(packageDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	packagePath := filepath.Join(packageDirectory, "surviving.sricsbackup")
	packed, err := plan.Run(ctx, l, packagePath)
	if err != nil {
		t.Fatal("package export", err)
	}
	if _, err = plan.Run(ctx, l, packagePath); err == nil {
		t.Fatal("existing package replaced")
	}
	unsafePackage := filepath.Join(c.Data, "unsafe.sricsbackup")
	if _, err = plan.Run(ctx, l, unsafePackage); err == nil {
		t.Fatal("export into library accepted")
	}
	foreignPackageKey := *key
	foreignPackageKey.RepositoryID = strings.Repeat("b", 64)
	foreignPackageFile := filepath.Join(base, "wrong-package-key.json")
	foreignPackageBytes, _ := json.Marshal(foreignPackageKey)
	if err = os.WriteFile(foreignPackageFile, foreignPackageBytes, 0600); err != nil {
		t.Fatal(err)
	}
	wrongPackageDest := filepath.Join(base, "wrong-package-import")
	if _, err = openRecoveryPackage(ctx, configPath, binary, recoveryRequest{Source: recoverySource{Target: "package", PackageFile: packagePath, RecoveryKeyFile: foreignPackageFile}, Directory: wrongPackageDest}); err == nil {
		t.Fatal("wrong package key accepted")
	}
	if _, err = os.Stat(wrongPackageDest); !os.IsNotExist(err) {
		t.Fatal("wrong key allocated output")
	}
	futureID = packed.Snapshot
	archive, err := zip.OpenReader(packagePath)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range archive.File {
		if entry.Method != zip.Store {
			t.Fatal("package recompressed ciphertext")
		}
		r, e := entry.Open()
		if e != nil {
			t.Fatal(e)
		}
		b, e := io.ReadAll(r)
		r.Close()
		if e != nil {
			t.Fatal(e)
		}
		if bytes.Contains(b, []byte(key.Secret)) || bytes.Contains(b, plain) || bytes.Contains(b, []byte("整机丢失后仍可取回的正文")) || bytes.Contains(b, []byte("synthetic-original-backup-password")) {
			t.Fatal("package contains plaintext or recovery secret")
		}
	}
	archive.Close()
	if err = l.Close(); err != nil {
		t.Fatal(err)
	}
	if err = os.RemoveAll(machine); err != nil {
		t.Fatal(err)
	}
	if err = os.RemoveAll(c.BackupRepository); err != nil {
		t.Fatal(err)
	}
	source = recoverySource{Target: "package", PackageFile: packagePath, RecoveryKeyFile: req.File}
	client = backup.Client{}
	req.VaultPassword = ""
	wrapped = nil
	identity = nil
	restored := filepath.Join(base, "restored")
	newConfigPath := filepath.Join(base, "new-machine", "config.json")
	if err = os.Mkdir(filepath.Dir(newConfigPath), 0700); err != nil {
		t.Fatal(err)
	}
	opened := recoveryCLI(t, ctx, binary, newConfigPath, "recovery-package-open", recoveryRequest{Source: source, Directory: filepath.Join(base, "imported-package")})
	if opened.Repository == "" || len(opened.Snapshots) != 1 || opened.Snapshots[0].ID != futureID {
		t.Fatal("package restore source incomplete")
	}
	source.Target, source.PackageFile, source.Repository = "local", "", opened.Repository
	source.RecoveryEnvelope = opened.RecoveryEnvelope
	r := recoveryRequest{Source: source, Directory: restored, Snapshot: futureID}
	history := recoveryCLI(t, ctx, binary, newConfigPath, "recovery-snapshots", r)
	if history.RecoveryKeyID != key.ID {
		t.Fatal("independent process did not load recovery file")
	}
	recovered := recoveryCLI(t, ctx, binary, newConfigPath, "recovery-restore", r)
	if recovered.Result == nil || !recovered.Result.VaultPresent {
		t.Fatal("missing recovery receipt")
	}
	items := recoveryCLI(t, ctx, binary, newConfigPath, "recovery-items", r)
	if len(items.Items) != 6 {
		t.Fatal("key-only listing did not recover all six modules")
	}
	seenModules := map[string]bool{}
	for _, it := range items.Items {
		seenModules[it.Module] = true
	}
	for _, module := range []string{"comics", "images", "photos", "novels", "private", "files"} {
		if !seenModules[module] {
			t.Fatal("missing recovered module", module)
		}
	}
	for id, expected := range originals {
		r.ItemID, r.Private, r.ExportDirectory = id, expected.private, base
		exported := recoveryCLI(t, ctx, binary, newConfigPath, "recovery-export", r)
		actual, e := os.ReadFile(exported.Exported)
		if e != nil {
			t.Fatal(e)
		}
		if expected.module == "comics" {
			z, e := zip.NewReader(bytes.NewReader(actual), int64(len(actual)))
			if e != nil || len(z.File) != len(expected.pages) {
				t.Fatal("comic ZIP incomplete", e)
			}
			for i, f := range z.File {
				reader, e := f.Open()
				if e != nil {
					t.Fatal(e)
				}
				p, e := io.ReadAll(reader)
				reader.Close()
				if e != nil || !bytes.Equal(p, expected.pages[i]) {
					t.Fatal("comic original bytes changed", e)
				}
			}
		} else if !bytes.Equal(actual, expected.pages[0]) {
			t.Fatal("recovered original bytes changed", expected.module)
		}
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
	exported = recoveryCLI(t, ctx, binary, newConfigPath, "recovery-export", r)
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
