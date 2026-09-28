package main

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func FuzzRecoveryKeyFile(f *testing.F) {
	key := recoveryKeyFile{Format: "srics-recovery-key", Version: 1, RepositoryID: strings.Repeat("a", 64), LibraryID: strings.Repeat("b", 32), CreatedAt: time.Unix(1, 0).UTC(), Secret: base64.RawURLEncoding.EncodeToString(make([]byte, 32))}
	key.ID = digest([]byte(key.Secret))
	seed, err := json.Marshal(key)
	if err != nil {
		f.Fatal(err)
	}
	f.Add(seed)
	f.Add([]byte(`{"format":"srics-recovery-key","version":999}`))
	f.Add([]byte(`null`))
	path := filepath.Join(f.TempDir(), "synthetic-recovery.json")
	f.Fuzz(func(t *testing.T, input []byte) {
		if len(input) > 20<<10 {
			t.Skip()
		}
		if err := os.WriteFile(path, input, 0600); err != nil {
			t.Fatal(err)
		}
		parsed, err := readRecoveryKey(path)
		if err != nil {
			return
		}
		secret, e := base64.RawURLEncoding.DecodeString(parsed.Secret)
		if e != nil || len(secret) != 32 || parsed.ID != digest([]byte(parsed.Secret)) || parsed.Version != 1 || parsed.Format != "srics-recovery-key" {
			t.Fatal("accepted malformed key")
		}
	})
}

func TestRecoveryKeyRejectsCorruptionAndInsecureFiles(t *testing.T) {
	key := recoveryKeyFile{Format: "srics-recovery-key", Version: 1, RepositoryID: strings.Repeat("a", 64), LibraryID: strings.Repeat("b", 32), CreatedAt: time.Now(), Secret: base64.RawURLEncoding.EncodeToString(make([]byte, 32))}
	key.ID = digest([]byte(key.Secret))
	path := filepath.Join(t.TempDir(), "key.json")
	write := func(k recoveryKeyFile) {
		t.Helper()
		b, err := json.Marshal(k)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(key)
	if _, err := readRecoveryKey(path); err != nil {
		t.Fatal(err)
	}
	bad := key
	bad.Secret = strings.Repeat("A", 43)
	bad.ID = strings.Repeat("f", 64)
	write(bad)
	if _, err := readRecoveryKey(path); err == nil {
		t.Fatal("corruption accepted")
	}
	bad = key
	bad.Version = 2
	write(bad)
	if _, err := readRecoveryKey(path); err == nil {
		t.Fatal("unknown format accepted")
	}
	write(key)
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := readRecoveryKey(path); err == nil {
		t.Fatal("world-readable key accepted")
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString("\n{}")
	f.Close()
	if _, err := readRecoveryKey(path); err == nil {
		t.Fatal("trailing content accepted")
	}
}
