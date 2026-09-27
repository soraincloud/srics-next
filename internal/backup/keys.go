package backup

import (
	"context"
	"encoding/json"
	"errors"
	"os"
)

// RepositoryID survives moving a repository to another disk or S3 endpoint.
func (c Client) RepositoryID(ctx context.Context) (string, error) {
	data, err := c.runLimited(ctx, "", 8192, "cat", "config")
	if err != nil {
		return "", errors.New("无法解锁备份仓库，请先完成一次备份并检查连接和口令")
	}
	var config struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(data, &config) != nil || !snapshotID.MatchString(config.ID) {
		return "", errors.New("备份仓库标识无效")
	}
	return config.ID, nil
}

// AddRecoveryPassword preserves existing keys. Retrying after cancellation is safe:
// a key already installed with this secret is verified rather than added again.
func (c Client) AddRecoveryPassword(ctx context.Context, secret, repositoryID string) error {
	if len(secret) < 32 {
		return errors.New("恢复密钥长度无效")
	}
	id, err := c.RepositoryID(ctx)
	if err != nil {
		return err
	}
	if id != repositoryID {
		return errors.New("恢复密钥与备份仓库不匹配")
	}
	recovery := c
	recovery.Password = secret
	if id, err := recovery.RepositoryID(ctx); err == nil && id == repositoryID {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	f, err := os.CreateTemp("", "srics-recovery-password-*")
	if err != nil {
		return errors.New("无法创建临时密钥文件")
	}
	defer os.Remove(f.Name())
	if _, err = f.WriteString(secret + "\n"); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if _, err = c.run(ctx, "", "key", "add", "--new-password-file", f.Name(), "--host", "srics-recovery", "--user", "recovery"); err != nil {
		return err
	}
	id, err = recovery.RepositoryID(ctx)
	if err != nil || id != repositoryID {
		return errors.New("恢复密钥未通过仓库解锁校验，请使用同一文件重试")
	}
	return nil
}
