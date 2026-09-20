package backup

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestS3ConfigurationBoundaries(t *testing.T) {
	valid := S3Config{Endpoint: "https://storage.example.com", Region: "us-east-1", Bucket: "srics-test", Prefix: "srics/main"}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []string{"http://storage.example.com", "https://key:secret@storage.example.com", "https://storage.example.com/bucket", "https://storage.example.com?token=secret", "https://storage.example.com#x", "s3:https://storage.example.com"} {
		c := valid
		c.Endpoint = endpoint
		if c.Validate() == nil {
			t.Fatal("unsafe endpoint accepted", endpoint)
		}
	}
	for _, prefix := range []string{"", "/", "../private", "a/../b", "a//b", "a/", "x?y", "x%2Fz"} {
		c := valid
		c.Prefix = prefix
		if c.Validate() == nil {
			t.Fatal("ambiguous prefix accepted", prefix)
		}
	}
}

func TestResticCredentialIsolation(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "restic-test")
	script := `#!/bin/sh
test "$AWS_ACCESS_KEY_ID" = synthetic-key || exit 1
test "$AWS_SECRET_ACCESS_KEY" = synthetic-secret || exit 1
test "$AWS_SESSION_TOKEN" = synthetic-token || exit 1
test -z "$AWS_ROLE_ARN$AWS_PROFILE$MINIO_ACCESS_KEY$DEBUG_LOG$RESTIC_PASSWORD_COMMAND" || exit 1
test "$AWS_SHARED_CREDENTIALS_FILE" = /dev/null || exit 1
test "$AWS_CONFIG_FILE" = /dev/null || exit 1
case "$*" in *synthetic-key*|*synthetic-secret*|*synthetic-token*|*synthetic-password*) exit 1 ;; esac
exit 0
`
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{"AWS_ROLE_ARN": "unexpected-role", "AWS_PROFILE": "other-account", "MINIO_ACCESS_KEY": "other-key", "DEBUG_LOG": filepath.Join(dir, "debug"), "RESTIC_PASSWORD_COMMAND": "unexpected-command"} {
		t.Setenv(k, v)
	}
	connection := S3Config{Endpoint: "https://storage.example.com", Region: "us-east-1", Bucket: "srics-test", Prefix: "srics/main"}
	c := Client{Binary: binary, Repository: connection.Repository(), Password: "synthetic-password", S3: &connection, Credentials: Credentials{AccessKeyID: "synthetic-key", SecretAccessKey: "synthetic-secret", SessionToken: "synthetic-token"}}
	if _, err := c.run(context.Background(), "", "cat", "config"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "debug")); !os.IsNotExist(err) {
		t.Fatal("debug log created")
	}
	c.Credentials.SecretAccessKey = "bad\nsecret"
	if _, err := c.run(context.Background(), "", "cat", "config"); err == nil || strings.Contains(err.Error(), "bad") {
		t.Fatal("invalid secret accepted or leaked")
	}
}
