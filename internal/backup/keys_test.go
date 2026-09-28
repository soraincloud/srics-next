package backup

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRecoveryPasswordNeverUsesPlaintextTemporaryFile(t *testing.T) {
	dir := t.TempDir()
	tmp := filepath.Join(dir, "temporary")
	if err := os.Mkdir(tmp, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", tmp)
	t.Setenv("SRICS_KEY_TEST_STATE", filepath.Join(dir, "installed"))
	const secret = "synthetic-recovery-secret-32-bytes-minimum"
	// Inspect the channel while the child is running, before a deferred removal
	// could hide a plaintext file. The state file records only whether add ran.
	script := `#!/bin/sh
case "$*" in *synthetic-recovery-secret*) exit 10 ;; esac
case "$*" in
  *"cat config"*)
    if [ "$RESTIC_PASSWORD" != synthetic-original-password ] && [ ! -f "$SRICS_KEY_TEST_STATE" ]; then exit 11; fi
    printf '{"id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}' ;;
  *"key add --new-password-file /dev/stdin"*)
    for entry in "$TMPDIR"/*; do [ ! -e "$entry" ] || exit 12; done
    IFS= read -r secret < /dev/stdin || exit 13
    [ "$secret" = synthetic-recovery-secret-32-bytes-minimum ] || exit 14
    printf installed > "$SRICS_KEY_TEST_STATE" ;;
  *) exit 15 ;;
esac
`
	binary := filepath.Join(dir, "restic-test")
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	c := Client{Binary: binary, Repository: filepath.Join(dir, "repo"), Password: "synthetic-original-password"}
	for range 2 { // A retry authenticates with the installed key without adding another.
		if err := c.AddRecoveryPassword(context.Background(), secret, strings.Repeat("a", 64)); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(tmp)
	if err != nil || len(entries) != 0 {
		t.Fatal("recovery password left a temporary file", err)
	}
}
