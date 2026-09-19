// Package backup runs restic without a shell. The M0 adapter accepts local
// repositories only; cloud credentials and scheduling are later milestones.
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
)

type Client struct{ Binary, Repository, Password string }

var snapshotID = regexp.MustCompile(`^[a-f0-9]{64}$`)

func (c Client) run(ctx context.Context, dir string, args ...string) ([]byte, error) {
	if !filepath.IsAbs(c.Repository) || len(c.Password) < 12 {
		return nil, errors.New("absolute local repository and strong test password required")
	}
	cmd := exec.CommandContext(ctx, c.Binary, append([]string{"--repo", c.Repository, "--no-cache"}, args...)...)
	cmd.Dir = dir
	for _, v := range os.Environ() {
		if !strings.HasPrefix(v, "RESTIC_") {
			cmd.Env = append(cmd.Env, v)
		}
	}
	cmd.Env = append(cmd.Env, "RESTIC_PASSWORD="+c.Password)
	var output bytes.Buffer
	cmd.Stdout = &output
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
	data, err := c.run(ctx, stage, "backup", "--json", "--host", "srics-verification", "--tag", "m0", ".")
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
