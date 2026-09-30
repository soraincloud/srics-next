package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/soraincloud/srics-next/internal/library"
	"github.com/soraincloud/srics-next/internal/media"
	"github.com/soraincloud/srics-next/internal/vault"
	"golang.org/x/crypto/bcrypt"
)

func migrationFixture(t *testing.T) (string, localConfig) {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "config", "config.json")
	if err := os.Mkdir(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	c := localConfig{Data: filepath.Join(root, "original"), Port: 19473}
	if err := applyConfig(path, configureRequest{Config: c, Password: syntheticPassword, VaultPassword: "synthetic-migration-vault"}); err != nil {
		t.Fatal(err)
	}
	return path, c
}
func TestMigrationPreservesCompleteLibrary(t *testing.T) {
	ctx := context.Background()
	path, c := migrationFixture(t)
	l, err := library.Open(c.Data)
	if err != nil {
		t.Fatal(err)
	}
	novel, err := l.CreateNovel(library.NewID(), "migration novel", []string{"tag"})
	if err != nil {
		t.Fatal(err)
	}
	chapter, err := l.CreateChapter(novel.ID, library.NewID(), "chapter", "original body", novel.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = l.SaveChapter(novel.ID, chapter.ID, "chapter", "revised body", chapter.Revision, false); err != nil {
		t.Fatal(err)
	}
	photo, err := os.ReadFile("../../testdata/media/lossless-bare.webp")
	if err != nil {
		t.Fatal(err)
	}
	up, err := l.CreateUpload(library.Upload{ID: library.NewID(), Module: "photos", Name: "photo", Files: []library.UploadFile{{Name: "original.webp", Size: int64(len(photo))}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = l.Receive(ctx, up.ID, 0, digest(photo), bytes.NewReader(photo), media.Converter{}); err != nil {
		t.Fatal(err)
	}
	item, err := l.Finish(up.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = l.Trash(item.ID, false); err != nil {
		t.Fatal(err)
	}
	// A received transfer chunk must remain resumable after migration.
	chunk := []byte("synthetic unfinished upload")
	pending, err := l.CreateUpload(library.Upload{ID: library.NewID(), Module: "photos", Name: "pending", Files: []library.UploadFile{{Name: "pending.bin", Size: int64(len(chunk))}}})
	if err != nil {
		t.Fatal(err)
	}
	tr := library.Transfer{ID: library.NewID(), Parent: pending.ID, Module: "photos", Name: "pending.bin", Size: int64(len(chunk)), Hashes: []string{digest(chunk)}}
	if _, err = l.CreateTransfer(ctx, tr, nil); err != nil {
		t.Fatal(err)
	}
	if _, err = l.ReceiveChunk(ctx, tr.ID, 0, bytes.NewReader(chunk), nil); err != nil {
		t.Fatal(err)
	}
	wrapped, _ := l.Setting("vault-key")
	key, err := vault.Unlock(wrapped, "synthetic-migration-vault")
	if err != nil {
		t.Fatal(err)
	}
	a := vault.NewAccess(ctx, key, time.Hour)
	defer a.Lock()
	original := []byte("synthetic private migration original")
	private, err := l.ReceivePrivate(ctx, a, library.NewID(), "files", "secret.txt", int64(len(original)), bytes.NewReader(original))
	if err != nil {
		t.Fatal(err)
	}
	k := recoveryKeyFile{Format: "srics-recovery-key", Version: 1, RepositoryID: strings.Repeat("a", 64), LibraryID: library.NewID(), CreatedAt: time.Now(), Secret: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{9}, 32))}
	k.ID = digest([]byte(k.Secret))
	recoveryWrapped, err := vault.Wrap(key, k.Secret)
	if err != nil {
		t.Fatal(err)
	}
	record := recoveryKeyRecord{Info: recoveryKeyInfo{ID: k.ID, RepositoryID: k.RepositoryID, State: "exported", CreatedAt: k.CreatedAt, VaultPresent: true}, WrappedVault: recoveryWrapped, VaultDigest: digest(wrapped)}
	recordJSON, _ := json.Marshal(record)
	keyJSON, _ := json.Marshal(k)
	keyPath := filepath.Join(filepath.Dir(c.Data), "recovery.json")
	if err = os.WriteFile(keyPath, keyJSON, 0600); err != nil {
		t.Fatal(err)
	}
	if err = l.SetSettings(map[string][]byte{"recovery-library-id": []byte(k.LibraryID), "recovery-key-" + k.ID: recordJSON}); err != nil {
		t.Fatal(err)
	}
	if err = l.Close(); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(filepath.Dir(c.Data), "moved")
	if err = migrateLibrary(ctx, path, dest); err != nil {
		t.Fatal(err)
	}
	got, _, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	expected := c
	expected.Data, err = resolvedPath(dest)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, expected) {
		t.Fatal("migration changed unrelated config", got, expected)
	}
	migrated, err := library.Open(dest)
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()
	login, _ := migrated.Setting("password")
	if bcrypt.CompareHashAndPassword(login, []byte(syntheticPassword)) != nil {
		t.Fatal("login changed")
	}
	afterWrapped, _ := migrated.Setting("vault-key")
	afterRecord, _ := migrated.Setting("recovery-key-" + k.ID)
	if !bytes.Equal(afterWrapped, wrapped) || !bytes.Equal(afterRecord, recordJSON) {
		t.Fatal("vault or recovery record changed")
	}
	recovered, err := recoveryKeyAccess(ctx, migrated, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Lock()
	r, _, err := migrated.PrivateRead(ctx, recovered, private, false)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := io.ReadAll(r)
	r.Close()
	if err != nil || !bytes.Equal(plain, original) {
		t.Fatal("private original changed", err)
	}
	actual, err := migrated.Chapter(novel.ID, chapter.ID)
	if err != nil || actual.Body != "revised body" {
		t.Fatal("chapter lost", err)
	}
	versions, err := migrated.Versions(novel.ID, chapter.ID)
	if err != nil || len(versions) != 1 {
		t.Fatal("history lost", err)
	}
	trash, err := migrated.Items("all", true)
	if err != nil || len(trash) != 1 {
		t.Fatal("trash lost", err)
	}
	tasks, err := migrated.Transfers(nil)
	if err != nil || len(tasks) != 1 || len(tasks[0].Done) != 1 {
		t.Fatal("upload progress lost", err)
	}
	if _, err = migrated.FinishTransfer(ctx, tr.ID, nil, media.Converter{}); err != nil {
		t.Fatal("upload not resumable", err)
	}
	for _, rel := range []string{filepath.Join("objects", item.Pages[0].Object), filepath.Join("private-objects", private.Object)} {
		old, _ := os.Stat(filepath.Join(c.Data, rel))
		new, _ := os.Stat(filepath.Join(dest, rel))
		if old == nil || new == nil || os.SameFile(old, new) {
			t.Fatal("copy missing or hardlinked", rel)
		}
	}
	originalLibrary, err := library.Open(c.Data)
	if err != nil {
		t.Fatal(err)
	}
	defer originalLibrary.Close()
	if err = originalLibrary.Verify(ctx); err != nil {
		t.Fatal("original damaged", err)
	}
	backups, _ := filepath.Glob(filepath.Join(filepath.Dir(path), "migrations", "*-config.json"))
	if len(backups) != 1 {
		t.Fatal("missing original configuration")
	}
	previous, _, err := loadConfig(backups[0])
	if err != nil || !reflect.DeepEqual(previous, c) {
		t.Fatal("invalid original configuration backup", err)
	}
}
func TestMigrationFailureKeepsOriginalConfiguration(t *testing.T) {
	for _, kind := range []string{"locked", "cancelled", "existing", "corrupted", "record-failure"} {
		t.Run(kind, func(t *testing.T) {
			path, c := migrationFixture(t)
			before, _ := os.ReadFile(path)
			dest := filepath.Join(filepath.Dir(c.Data), "moved")
			l, err := library.Open(c.Data)
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			if kind == "corrupted" {
				w, _ := l.Setting("vault-key")
				key, e := vault.Unlock(w, "synthetic-migration-vault")
				if e != nil {
					t.Fatal(e)
				}
				a := vault.NewAccess(ctx, key, time.Hour)
				it, e := l.ReceivePrivate(ctx, a, library.NewID(), "files", "secret", 6, strings.NewReader("secret"))
				a.Lock()
				if e != nil {
					t.Fatal(e)
				}
				if e = os.WriteFile(l.PrivatePath(it.Object), []byte("damaged ciphertext"), 0600); e != nil {
					t.Fatal(e)
				}
			}
			if kind != "locked" {
				l.Close()
			} else {
				defer l.Close()
			}
			if kind == "existing" {
				if err = os.Mkdir(dest, 0700); err != nil {
					t.Fatal(err)
				}
				os.WriteFile(filepath.Join(dest, "keep"), []byte("keep"), 0600)
			}
			if kind == "record-failure" {
				if err = os.WriteFile(filepath.Join(filepath.Dir(path), "migrations"), []byte("keep"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "cancelled" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if err = migrateLibrary(ctx, path, dest); err == nil {
				t.Fatal("unsafe migration succeeded")
			}
			after, _ := os.ReadFile(path)
			if !bytes.Equal(before, after) {
				t.Fatal("failed migration switched configuration")
			}
			if kind == "corrupted" || kind == "record-failure" {
				if _, err = os.Stat(dest); err != nil {
					t.Fatal("failed copy was discarded")
				}
			}
			if kind == "existing" {
				data, _ := os.ReadFile(filepath.Join(dest, "keep"))
				if string(data) != "keep" {
					t.Fatal("overwrote target")
				}
			}
		})
	}
}
func TestMigrationRejectsNestedPathsAndLinks(t *testing.T) {
	path, c := migrationFixture(t)
	alias := filepath.Join(filepath.Dir(c.Data), "alias")
	if err := os.Symlink(c.Data, alias); err != nil {
		t.Fatal(err)
	}
	for _, dest := range []string{c.Data, filepath.Join(c.Data, "nested"), filepath.Join(alias, "nested"), filepath.Join(filepath.Dir(path), "nested"), "relative", "/"} {
		if _, err := migrationDestination(path, c, dest); err == nil {
			t.Fatal("unsafe path allowed", dest)
		}
	}
	source := t.TempDir()
	if err := os.Symlink(path, filepath.Join(source, "link")); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "copy")
	if err := copyMigrationSnapshot(context.Background(), source, dest); err == nil {
		t.Fatal("copy followed symlink")
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatal("invalid snapshot published destination")
	}
}
