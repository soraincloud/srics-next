//go:build integration

package server

import (
	"context"
	"encoding/json"
	"filippo.io/age"
	"github.com/soraincloud/srics-next/internal/backup"
	"github.com/soraincloud/srics-next/internal/library"
	"github.com/soraincloud/srics-next/internal/media"
	"github.com/soraincloud/srics-next/internal/recoverykey"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestUnifiedBackgroundBackupFailurePreservesPreviousSuccess(t *testing.T) {
	binary, err := exec.LookPath("restic")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	base := t.TempDir()
	data := filepath.Join(base, "library")
	if err = library.Create(data); err != nil {
		t.Fatal(err)
	}
	l, err := library.Open(data)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	key, _ := age.GenerateX25519Identity()
	now := time.Now().UTC()
	root := recoverykey.Record{ID: recoverykey.Fingerprint(key.Recipient().String()), LibraryID: library.NewID(), Recipient: key.Recipient().String(), CreatedAt: now, State: "verified", VerifiedAt: &now}
	encoded, _ := json.Marshal(root)
	if err = l.SetSettings(map[string][]byte{recoverykey.Setting: encoded, "recovery-library-id": []byte(root.LibraryID), "password": []byte("synthetic-hash")}); err != nil {
		t.Fatal(err)
	}
	c := backup.UnifiedConfig{Repository: filepath.Join(base, "cache", "repository"), PasswordFile: filepath.Join(base, "cache", "key"), Directory: filepath.Join(base, "output")}
	os.Mkdir(filepath.Dir(c.Repository), 0700)
	os.Mkdir(c.Directory, 0700)
	os.WriteFile(c.PasswordFile, []byte("synthetic-background-key"), 0600)
	plan := backup.Unified{Config: c, Binary: binary}
	client, err := plan.Client()
	if err != nil {
		t.Fatal(err)
	}
	if err = client.Init(ctx); err != nil {
		t.Fatal(err)
	}
	s := New(ctx, "127.0.0.1:0", nil, nil)
	s.EnableLibrary(l, media.Converter{}, backup.Client{})
	s.EnableUnifiedBackup(plan)
	wait := func() {
		t.Helper()
		for {
			s.library.backupMu.Lock()
			running := s.library.backupActive
			s.library.backupMu.Unlock()
			if !running {
				return
			}
			select {
			case <-ctx.Done():
				t.Fatal("backup never completed")
			case <-time.After(20 * time.Millisecond):
			}
		}
	}
	if err = s.library.startBackup(); err != nil {
		t.Fatal(err)
	}
	wait()
	passed, err := s.library.readUnifiedRecord()
	if err != nil || passed.Status != "passed" || !passed.ReadVerified || passed.File == "" {
		t.Fatal("missing successful package", passed, err)
	}
	m, err := backup.InspectPackage(passed.File)
	if err != nil || m.Version != 2 || m.RecoveryKeyID != root.ID {
		t.Fatal("wrong format", err)
	}
	moved := filepath.Join(base, "offline")
	if err = os.Rename(c.Directory, moved); err != nil {
		t.Fatal(err)
	}
	if err = s.library.startBackup(); err != nil {
		t.Fatal(err)
	}
	wait()
	failed, err := s.library.readUnifiedRecord()
	if err != nil || failed.Status != "failed" || failed.Snapshot != passed.Snapshot || failed.SavedAt != passed.SavedAt {
		t.Fatal("failure erased prior successful backup", failed, err)
	}
	if _, err = os.Stat(c.Directory); !os.IsNotExist(err) {
		t.Fatal("recreated disconnected target")
	}
	if _, err = os.Stat(filepath.Join(moved, filepath.Base(passed.File))); err != nil {
		t.Fatal("old file disappeared")
	}
	if _, err = s.library.retentionPreview(ctx, "local"); err == nil {
		t.Fatal("legacy cleanup accepted for package mode")
	}
}
