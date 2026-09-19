//go:build integration

package verification

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/soraincloud/srics-next/internal/backup"
)

func TestFullRecoveryRehearsal(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	report := Run(ctx, ResolveTools(), nil)
	if report.Status != "passed" {
		t.Fatalf("recovery failed: %+v", report.Checks)
	}
	for _, check := range report.Checks {
		if check.Status != "passed" {
			t.Fatalf("incomplete step: %+v", check)
		}
	}
}
func TestResticRejectsWrongPasswordAndExistingRestore(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	root := t.TempDir()
	tools := ResolveTools()
	if tools.Restic == "" {
		t.Fatal("restic required")
	}
	client := backup.Client{Binary: tools.Restic, Repository: filepath.Join(root, "repo"), Password: randomSecret()}
	if err := client.Init(ctx); err != nil {
		t.Fatal(err)
	}
	wrong := client
	wrong.Password = randomSecret()
	if err := wrong.Check(ctx); err == nil {
		t.Fatal("wrong backup password accepted")
	}
	source := filepath.Join(root, "stage")
	os.Mkdir(source, 0700)
	os.WriteFile(filepath.Join(source, "test"), []byte("fixture"), 0600)
	id, err := client.Backup(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	if err = client.Restore(ctx, id, source); err == nil {
		t.Fatal("restore overwrote an existing target")
	}
}
