//go:build integration

package backup

import (
	"bytes"
	"context"
	"encoding/pem"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestUnifiedCloudPackagePublicationReadbackAndFailures(t *testing.T) {
	fixture := &s3Fixture{objects: map[string][]byte{}, bucket: true}
	s := httptest.NewTLSServer(fixture)
	defer s.Close()
	root := t.TempDir()
	ca := filepath.Join(root, "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: s.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	connection := S3Config{Endpoint: s.URL, Region: "us-east-1", Bucket: "srics-test", Prefix: "srics/backups", Lookup: "path", CAFile: ca}
	c := Client{S3: &connection, Credentials: Credentials{AccessKeyID: "synthetic-s3-key", SecretAccessKey: "synthetic-s3-secret"}}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := c.ProbePackages(ctx); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "SRICS-fixture.sricsbackup")
	content := bytes.Repeat([]byte("synthetic-encrypted-package"), 1000)
	if err := os.WriteFile(file, content, 0600); err != nil {
		t.Fatal(err)
	}
	object, err := c.UploadPackage(ctx, file)
	if err != nil || object != "s3://srics-test/srics/backups/SRICS-fixture.sricsbackup" {
		t.Fatal(object, err)
	}
	fixture.mu.Lock()
	stored := append([]byte(nil), fixture.objects["srics/backups/SRICS-fixture.sricsbackup"]...)
	fixture.mu.Unlock()
	if !bytes.Equal(stored, content) {
		t.Fatal("cloud bytes changed")
	}
	if _, err = c.UploadPackage(ctx, file); err == nil {
		t.Fatal("overwrote old cloud backup")
	}
	other := filepath.Join(root, "SRICS-corrupt.sricsbackup")
	os.WriteFile(other, content, 0600)
	fixture.mu.Lock()
	fixture.corrupt = true
	fixture.mu.Unlock()
	if _, err = c.UploadPackage(ctx, other); err == nil {
		t.Fatal("corrupted remote read accepted")
	}
	fixture.mu.Lock()
	fixture.corrupt = false
	fixture.denyRead = true
	fixture.mu.Unlock()
	if _, err = c.UploadPackage(ctx, filepath.Join(root, "SRICS-corrupt.sricsbackup")); err == nil {
		t.Fatal("read permission failure accepted")
	}
	fixture.mu.Lock()
	fixture.denyRead = false
	fixture.denyWrite = true
	fixture.mu.Unlock()
	third := filepath.Join(root, "SRICS-denied.sricsbackup")
	os.WriteFile(third, content, 0600)
	if _, err = c.UploadPackage(ctx, third); err == nil {
		t.Fatal("write denial accepted")
	}
	fixture.mu.Lock()
	fixture.denyWrite = false
	fixture.objects["srics/backups/config"] = []byte("old-restic")
	fixture.mu.Unlock()
	if err = c.ProbePackages(ctx); err == nil {
		t.Fatal("legacy restic prefix accepted")
	}
}
