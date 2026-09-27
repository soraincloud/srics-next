package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/soraincloud/srics-next/internal/atomicfile"
	"github.com/soraincloud/srics-next/internal/backup"
	"github.com/soraincloud/srics-next/internal/library"
	"golang.org/x/sys/unix"
)

const recoveryReceipt = ".srics-recovery.json"

type recoverySource struct {
	RecoveryKeyFile string      `json:"recoveryKeyFile"`
	Target          string      `json:"target"`
	Repository      string      `json:"repository"`
	PasswordFile    string      `json:"passwordFile"`
	Cloud           cloudConfig `json:"cloud"`
}
type recoveryRequest struct {
	NewPassword      string         `json:"newPassword"`
	NewVaultPassword string         `json:"newVaultPassword"`
	VaultPassword    string         `json:"vaultPassword"`
	ItemID           string         `json:"itemID"`
	Private          bool           `json:"private"`
	ExportDirectory  string         `json:"exportDirectory"`
	Source           recoverySource `json:"source"`
	Snapshot         string         `json:"snapshot"`
	Directory        string         `json:"directory"`
}
type recoveryResult struct {
	Version      int       `json:"version"`
	Snapshot     string    `json:"snapshot"`
	Repository   string    `json:"repository"` // Digest only, no credentials or endpoint.
	Directory    string    `json:"directory"`
	VerifiedAt   time.Time `json:"verifiedAt"`
	VaultPresent bool      `json:"vaultPresent"`
}
type recoveryResponse struct {
	RecoveryKeyID  string            `json:"recoveryKeyID,omitempty"`
	PasswordsReset bool              `json:"passwordsReset"`
	Items          []recoveryItem    `json:"items,omitempty"`
	Exported       string            `json:"exported,omitempty"`
	Snapshots      []backup.Snapshot `json:"snapshots,omitempty"`
	Result         *recoveryResult   `json:"result,omitempty"`
	Activated      bool              `json:"activated"`
}

func (s recoverySource) client(ctx context.Context, binary string) (backup.Client, error) {
	if s.RecoveryKeyFile != "" {
		return s.recoveryKeyClient(ctx, binary)
	}
	if s.Target == "cloud" {
		s.Cloud.Enabled = true
		return configuredCloud(localConfig{Cloud: s.Cloud}, binary)
	}
	if s.Target != "local" || !filepath.IsAbs(s.Repository) {
		return backup.Client{}, errors.New("请选择本地备份目录或云端存储")
	}
	if err := outsideDirectories(s.PasswordFile, s.Repository); err != nil {
		return backup.Client{}, errors.New("备份口令文件需放在备份仓库之外")
	}
	data, err := privateFile(s.PasswordFile, 4096)
	if err != nil {
		return backup.Client{}, err
	}
	defer clear(data)
	password := strings.TrimRight(string(data), "\r\n")
	if len(password) < 12 {
		return backup.Client{}, errors.New("备份口令至少需要 12 字节")
	}
	return backup.Client{Binary: binary, Repository: s.Repository, Password: password}, nil
}
func recoveryDirectory(dest string, protected ...string) (string, error) {
	if !filepath.IsAbs(dest) || filepath.Clean(dest) == string(os.PathSeparator) {
		return "", errors.New("请选择新建恢复目录的绝对路径")
	}
	dest, err := resolvedPath(dest)
	if err != nil {
		return "", err
	}
	for _, dir := range protected {
		if dir == "" {
			continue
		}
		root, err := resolvedPath(dir)
		if err != nil {
			return "", err
		}
		rel, err := filepath.Rel(root, dest)
		if err != nil || rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))) {
			return "", errors.New("恢复目录必须位于当前资料库、备份仓库和程序配置目录之外")
		}
	}
	return dest, nil
}
func restoreVerified(ctx context.Context, c backup.Client, id, dest string, protected ...string) (*recoveryResult, error) {
	if c.S3 == nil {
		protected = append(protected, c.Repository)
	}
	dest, err := recoveryDirectory(dest, protected...)
	if err != nil {
		return nil, err
	}
	if _, err = os.Lstat(dest); !os.IsNotExist(err) {
		return nil, errors.New("恢复目录已存在或无法访问，请选择新目录")
	}
	parent, err := os.Stat(filepath.Dir(dest))
	if err != nil || !parent.IsDir() {
		return nil, errors.New("恢复位置不存在，请先连接目标磁盘")
	}
	listCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
	snapshots, err := c.LibrarySnapshots(listCtx)
	cancel()
	if err != nil {
		return nil, err
	}
	found := false
	for _, s := range snapshots {
		if s.ID == id {
			found = true
			break
		}
	}
	if !found {
		return nil, errors.New("未找到所选资料库快照，请重新读取备份历史")
	}
	if err = c.Restore(ctx, id, dest); err != nil {
		return nil, errors.New("恢复未完成，请检查备份、口令或目标磁盘；已生成的目录保留，重试请选择新目录")
	}
	l, err := library.Open(dest)
	if err != nil {
		return nil, errors.New("恢复文件无法作为资料库打开；目录已保留供检查")
	}
	verifyErr := l.Verify(ctx)
	wrapped, keyErr := l.Setting("vault-key")
	closeErr := l.Close()
	if verifyErr != nil || keyErr != nil || closeErr != nil {
		return nil, errors.New("恢复后的索引或文件校验未通过；目录已保留供检查")
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	result := &recoveryResult{Version: 1, Snapshot: id, Repository: fmt.Sprintf("%x", sha256.Sum256([]byte(c.Repository))), Directory: dest, VerifiedAt: time.Now().UTC(), VaultPresent: len(wrapped) > 0}
	if err = atomicfile.WriteNew(filepath.Join(dest, recoveryReceipt), func(w io.Writer) error { return json.NewEncoder(w).Encode(result) }); err != nil {
		return nil, errors.New("无法保存恢复校验记录；请检查目标磁盘")
	}
	return result, nil
}

func activateRecovery(ctx context.Context, configPath, directory string) (*recoveryResult, error) {
	c, _, err := loadConfig(configPath)
	if err != nil {
		return nil, err
	}
	if runtime.GOOS == "darwin" && serviceLoaded(ctx, configPath) {
		return nil, errors.New("请先停止服务，再启用恢复的资料库")
	}
	// Keep the old library locked even if its index is damaged or missing.
	lock, lockErr := os.OpenFile(filepath.Join(c.Data, ".lock"), os.O_RDWR, 0)
	if lockErr == nil {
		defer lock.Close()
		if err = unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
			return nil, errors.New("当前资料库正在使用，请先停止服务")
		}
		defer unix.Flock(int(lock.Fd()), unix.LOCK_UN)
	} else if !os.IsNotExist(lockErr) {
		return nil, errors.New("无法检查当前资料库是否已停止")
	}

	dest, err := recoveryDirectory(directory, c.Data, c.BackupRepository, filepath.Dir(configPath))
	if err != nil {
		return nil, err
	}
	result, l, err := checkedRecovery(ctx, dest)
	if err != nil {
		return nil, err
	}
	defer l.Close()
	password, err := l.Setting("password")
	if err != nil || len(password) == 0 {
		return nil, errors.New("恢复的资料库没有登录密码，未启用")
	}
	c.Data = dest
	if err = c.validate(); err != nil {
		return nil, err
	}
	// Switching sources must not require a currently accessible cloud or backup disk.
	if err = writeConfig(configPath, c); err != nil {
		return nil, err
	}
	return result, nil
}
func checkedRecovery(ctx context.Context, dest string) (*recoveryResult, *library.Library, error) {
	data, err := privateFile(filepath.Join(dest, recoveryReceipt), 8192)
	if err != nil {
		return nil, nil, errors.New("缺少本机恢复校验记录，请先完成恢复")
	}
	var result recoveryResult
	if json.Unmarshal(data, &result) != nil || result.Version != 1 || result.VerifiedAt.IsZero() || len(result.Snapshot) != 64 {
		return nil, nil, errors.New("恢复校验记录无效")
	}
	if _, err := hex.DecodeString(result.Snapshot); err != nil {
		return nil, nil, errors.New("恢复快照编号无效")
	}
	original, err := resolvedPath(result.Directory)
	if err != nil || original != dest {
		return nil, nil, errors.New("恢复目录已移动，请先重新验证恢复")
	}
	l, err := library.Open(dest)
	if err != nil {
		return nil, nil, err
	}
	if err = l.Verify(ctx); err != nil {
		l.Close()
		return nil, nil, errors.New("恢复目录校验失败，未切换资料库")
	}
	return &result, l, nil
}
func recoveryManager(ctx context.Context, action, path, binary string, input io.Reader) error {
	var request recoveryRequest
	d := json.NewDecoder(io.LimitReader(input, 65537))
	d.DisallowUnknownFields()
	if d.Decode(&request) != nil || d.Decode(&struct{}{}) != io.EOF {
		return errors.New("恢复请求格式不正确")
	}
	var response recoveryResponse
	var err error
	if action == "recovery-reset-passwords" {
		response, err = resetRecoveryPasswords(ctx, path, request)
	} else if action == "recovery-items" || action == "recovery-export" {
		response, err = recoveryExportRequest(ctx, path, request)
	} else if action == "recovery-activate" {
		response.Result, err = activateRecovery(ctx, path, request.Directory)
		response.Activated = err == nil
	} else if action == "recovery-inspect" {
		c, _, e := loadConfig(path)
		if e != nil {
			return e
		}
		dest, e := recoveryDirectory(request.Directory, c.Data, c.BackupRepository, filepath.Dir(path))
		if e != nil {
			return e
		}
		var l *library.Library
		response.Result, l, err = checkedRecovery(ctx, dest)
		if l != nil {
			if e = l.Close(); err == nil {
				err = e
			}
		}
	} else {
		client, e := request.Source.client(ctx, binary)
		if e != nil {
			return e
		}
		if request.Source.RecoveryKeyFile != "" {
			key, e := readRecoveryKey(request.Source.RecoveryKeyFile)
			if e != nil {
				return e
			}
			response.RecoveryKeyID = key.ID
		}
		switch action {
		case "recovery-snapshots":
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, 45*time.Second)
			defer cancel()
			response.Snapshots, err = client.LibrarySnapshots(ctx)
		case "recovery-restore":
			if response.RecoveryKeyID != "" {
				snapshots, e := client.LibrarySnapshots(ctx)
				if e != nil {
					return e
				}
				supported := false
				for _, snapshot := range snapshots {
					if snapshot.ID == request.Snapshot && slices.Contains(snapshot.RecoveryKeys, response.RecoveryKeyID) {
						supported = true
					}
				}
				if !supported {
					return errors.New("此恢复点未记录所选恢复密钥，请选择标记为支持的恢复点")
				}
			}
			c, _, e := loadConfig(path)
			if e != nil {
				return e
			}
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, 24*time.Hour)
			defer cancel()
			response.Result, err = restoreVerified(ctx, client, request.Snapshot, request.Directory, c.Data, c.BackupRepository, filepath.Dir(path))
		default:
			return errors.New("未知恢复操作")
		}
	}
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(response)
}
