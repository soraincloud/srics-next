// Package backup runs restic without a shell for local and S3 repositories.
package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

type Client struct {
	Binary, Repository, Password string
	S3                           *S3Config
	Credentials                  Credentials
}

var snapshotID = regexp.MustCompile(`^[a-f0-9]{64}$`)

func (c Client) run(ctx context.Context, dir string, args ...string) ([]byte, error) {
	return c.runLimited(ctx, dir, 0, args...)
}
func (c Client) runLimited(ctx context.Context, dir string, limit int, args ...string) ([]byte, error) {
	if len(c.Password) < 12 {
		return nil, errors.New("备份口令至少需要 12 字节")
	}
	options := []string{"--repo", c.Repository, "--no-cache"}
	if c.S3 != nil {
		if err := c.S3.Validate(); err != nil {
			return nil, err
		}
		if err := c.Credentials.Validate(); err != nil {
			return nil, err
		}
		if c.Repository != c.S3.Repository() {
			return nil, errors.New("S3 仓库与连接配置不一致")
		}
		options = append(options, "-o", "s3.region="+c.S3.Region, "-o", "s3.bucket-lookup="+c.S3.lookup())
		if c.S3.CAFile != "" {
			options = append(options, "--cacert", c.S3.CAFile)
		}
	} else if !filepath.IsAbs(c.Repository) {
		return nil, errors.New("备份目录需为绝对路径")
	}
	cmd := exec.CommandContext(ctx, c.Binary, append(options, args...)...)
	// Let restic release its repository lock on cancellation before force killing.
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = 5 * time.Second
	cmd.Dir = dir
	for _, v := range os.Environ() {
		// An inherited AWS role/token or debug sink must never override this target.
		key, _, _ := strings.Cut(v, "=")
		if !strings.HasPrefix(key, "RESTIC_") && !strings.HasPrefix(key, "AWS_") && !strings.HasPrefix(key, "MINIO_") && key != "DEBUG_LOG" && key != "DEBUG_FILES" && key != "DEBUG_FUNCS" {
			cmd.Env = append(cmd.Env, v)
		}
	}
	cmd.Env = append(cmd.Env, "RESTIC_PASSWORD="+c.Password)
	if c.S3 != nil {
		cmd.Env = append(cmd.Env, "AWS_ACCESS_KEY_ID="+c.Credentials.AccessKeyID, "AWS_SECRET_ACCESS_KEY="+c.Credentials.SecretAccessKey, "AWS_SESSION_TOKEN="+c.Credentials.SessionToken, "AWS_SHARED_CREDENTIALS_FILE="+os.DevNull, "AWS_CONFIG_FILE="+os.DevNull, "AWS_EC2_METADATA_DISABLED=true")
	}
	var output bytes.Buffer
	cmd.Stdout = &output
	if limit > 0 {
		cmd.Stdout = &limitedOutput{buffer: &output, remaining: limit}
	}
	// Do not forward restic stderr or command environment into reports/logs.
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("restic %s failed: %w", args[0], err)
	}
	return output.Bytes(), nil
}
func (c Client) Init(ctx context.Context) error {
	_, err := c.run(ctx, "", "init", "--repository-version", "2")
	return err
}
func (c Client) Backup(ctx context.Context, stage string) (string, error) {
	return c.backup(ctx, stage, "srics-verification", "m0")
}
func (c Client) BackupLibrary(ctx context.Context, stage string) (string, error) {
	tags := []string{"library-v1"}
	data, err := os.ReadFile(filepath.Join(stage, "recovery-keys.json"))
	if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	if len(data) > 0 {
		var ids []string
		if json.Unmarshal(data, &ids) != nil {
			return "", errors.New("恢复密钥清单无效")
		}
		for _, id := range ids {
			if !snapshotID.MatchString(id) {
				return "", errors.New("恢复密钥标识无效")
			}
			tags = append(tags, "recovery-key:"+id)
		}
	}
	return c.backup(ctx, stage, "srics-library", tags...)
}
func (c Client) backup(ctx context.Context, stage, host string, tags ...string) (string, error) {
	args := []string{"backup", "--json", "--host", host}
	for _, tag := range tags {
		args = append(args, "--tag", tag)
	}
	args = append(args, ".")
	data, err := c.run(ctx, stage, args...)
	if err != nil {
		return "", err
	}
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		var msg struct {
			Type string `json:"message_type"`
			ID   string `json:"snapshot_id"`
		}
		if json.Unmarshal(line, &msg) == nil && msg.Type == "summary" && snapshotID.MatchString(msg.ID) {
			return msg.ID, nil
		}
	}
	return "", errors.New("restic did not return a complete snapshot id")
}
func (c Client) Check(ctx context.Context) error {
	_, err := c.run(ctx, "", "check", "--read-data")
	return err
}
func (c Client) Restore(ctx context.Context, id, dest string) error {
	if !snapshotID.MatchString(id) {
		return errors.New("invalid snapshot id")
	}
	// Require a new target: never merge a restored snapshot into existing data.
	if err := os.Mkdir(dest, 0700); err != nil {
		return err
	}
	_, err := c.run(ctx, "", "restore", id, "--target", dest, "--verify")
	return err
}
