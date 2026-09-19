package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/soraincloud/srics-next/internal/backup"
	"github.com/soraincloud/srics-next/internal/library"
	"golang.org/x/crypto/bcrypt"
)

type localConfig struct {
	Data               string `json:"data"`
	Port               int    `json:"port"`
	BackupRepository   string `json:"backupRepository"`
	BackupPasswordFile string `json:"backupPasswordFile"`
}
type configureRequest struct {
	Config               localConfig `json:"config"`
	Password             string      `json:"password"`
	CurrentPassword      string      `json:"currentPassword"`
	VaultPassword        string      `json:"vaultPassword"`
	CurrentVaultPassword string      `json:"currentVaultPassword"`
	VaultIdleMinutes     int         `json:"vaultIdleMinutes"`
}

func configPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "SRICS Next", "config.json"), nil
}
func loadConfig(path string) (localConfig, bool, error) {
	c := localConfig{Data: filepath.Join(filepath.Dir(path), "library"), Port: 19473}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return c, false, nil
	}
	if err != nil {
		return c, false, err
	}
	if err = json.Unmarshal(b, &c); err != nil {
		return c, true, errors.New("本机配置文件无法读取")
	}
	return c, true, c.validate()
}
func (c localConfig) address() string { return net.JoinHostPort("127.0.0.1", strconv.Itoa(c.Port)) }
func (c localConfig) validate() error {
	if !filepath.IsAbs(c.Data) || filepath.Clean(c.Data) == "/" {
		return errors.New("请选择资料目录的绝对路径")
	}
	if c.Port < 1024 || c.Port > 65535 {
		return errors.New("端口需在 1024–65535 之间")
	}
	if c.BackupRepository == "" && c.BackupPasswordFile == "" {
		return nil
	}
	if !filepath.IsAbs(c.BackupRepository) || !filepath.IsAbs(c.BackupPasswordFile) {
		return errors.New("备份目录和口令文件需要同时配置，使用绝对路径")
	}
	for _, pair := range [][2]string{{c.Data, c.BackupRepository}, {c.BackupRepository, c.Data}, {c.Data, c.BackupPasswordFile}, {c.BackupRepository, c.BackupPasswordFile}} {
		a, err := resolvedPath(pair[0])
		if err != nil {
			return err
		}
		b, err := resolvedPath(pair[1])
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(a, b)
		if err != nil || rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))) {
			return errors.New("资料目录、备份目录不能相互包含；口令文件必须单独存放")
		}
	}
	return nil
}

// Resolve existing parents as well, so a symlink cannot hide overlapping paths.
func resolvedPath(path string) (string, error) {
	p, err := filepath.EvalSymlinks(path)
	if err == nil {
		return p, nil
	}
	if !os.IsNotExist(err) {
		return "", err
	}
	parent := filepath.Dir(path)
	if parent == path {
		return "", err
	}
	p, err = resolvedPath(parent)
	return filepath.Join(p, filepath.Base(path)), err
}
func configuredBackup(c localConfig, binary string) (backup.Client, error) {
	client := backup.Client{Binary: binary, Repository: c.BackupRepository}
	if c.BackupRepository == "" && c.BackupPasswordFile == "" {
		return client, nil
	}
	if err := c.validate(); err != nil {
		return client, err
	}
	f, err := os.Open(c.BackupPasswordFile)
	if err != nil {
		return client, errors.New("无法读取备份口令文件")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return client, errors.New("备份口令文件需为仅本人可读的普通文件（权限 600）")
	}
	b, err := io.ReadAll(io.LimitReader(f, 4097))
	defer clear(b)
	if err != nil || len(b) > 4096 {
		return client, errors.New("备份口令文件无法读取或过大")
	}
	client.Password = strings.TrimRight(string(b), "\r\n")
	if len(client.Password) < 12 {
		return client, errors.New("备份口令至少需要 12 字节")
	}
	return client, nil
}
func writeConfig(path string, c localConfig) error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".config-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err = f.Write(append(b, '\n')); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	d, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
func applyConfig(path string, req configureRequest) error {
	c := req.Config
	c.Data = filepath.Clean(c.Data)
	if err := c.validate(); err != nil {
		return err
	}
	previous, exists, err := loadConfig(path)
	if err != nil {
		return err
	}
	if exists && previous.Data != c.Data {
		return errors.New("资料目录已固定；迁移或恢复需单独操作，配置不会移动或覆盖资料")
	}
	if _, err := configuredBackup(c, ""); err != nil {
		return err
	}
	if req.Password != "" && (utf8.RuneCountInString(req.Password) < 12 || len(req.Password) > 72) {
		return errors.New("登录密码至少 12 个字符，最多 72 字节")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	// A saved configuration pins an existing library. Never recreate it if missing.
	if !exists {
		if _, err := os.Stat(c.Data); os.IsNotExist(err) {
			if req.Password == "" {
				return errors.New("请设置登录密码")
			}
			// Preserve the old installation marker's missing-volume protection.
			if c.Data == previous.Data {
				if _, e := os.Stat(filepath.Join(filepath.Dir(path), "initialized")); e == nil {
					return errors.New("已初始化的资料目录缺失，请检查数据盘")
				} else if !os.IsNotExist(e) {
					return e
				}
			}
			if err = library.Create(c.Data); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
	}
	l, err := library.Open(c.Data)
	if err != nil {
		return fmt.Errorf("无法打开资料目录，请先停止服务：%w", err)
	}
	defer l.Close()
	oldHash, err := l.Setting("password")
	if err != nil {
		return err
	}
	if len(oldHash) == 0 && req.Password == "" {
		return errors.New("请设置登录密码")
	}
	if len(oldHash) > 0 && req.Password != "" && bcrypt.CompareHashAndPassword(oldHash, []byte(req.CurrentPassword)) != nil {
		return errors.New("当前登录密码不正确，未修改配置")
	}
	var newHash []byte
	if req.Password != "" {
		newHash, err = bcrypt.GenerateFromPassword([]byte(req.Password), 12)
		if err != nil {
			return err
		}
	}
	var wrapped []byte
	if req.VaultPassword != "" {
		wrapped, err = l.PrepareVault(req.VaultPassword, req.CurrentVaultPassword)
		if err != nil {
			return err
		}
	}
	if req.VaultIdleMinutes < 0 || req.VaultIdleMinutes > 60 {
		return errors.New("自动锁定时间需为 1–60 分钟")
	}
	// Publish paths before updating credentials; a failed configuration write
	// must never change the existing login password.
	if err := writeConfig(path, c); err != nil {
		return err
	}
	if len(wrapped) > 0 {
		if err = l.SetSetting("vault-key", wrapped); err != nil {
			return err
		}
	}
	if req.VaultIdleMinutes > 0 {
		if err = l.SetSetting("vault-idle", []byte(strconv.Itoa(req.VaultIdleMinutes))); err != nil {
			return err
		}
	}
	if len(newHash) > 0 {
		if len(oldHash) == 0 {
			return l.Setup(newHash)
		}
		return l.SetSetting("password", newHash)
	}
	return nil
}
