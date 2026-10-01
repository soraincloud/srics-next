package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/soraincloud/srics-next/internal/atomicfile"
	"github.com/soraincloud/srics-next/internal/backup"
	"github.com/soraincloud/srics-next/internal/library"
)

type backupSetupRequest struct {
	Target       string              `json:"target"`
	Mode         string              `json:"mode"`
	Repository   string              `json:"repository"`
	Cloud        cloudConfig         `json:"cloud"`
	Credentials  *backup.Credentials `json:"credentials,omitempty"`
	Password     string              `json:"password"`
	PasswordFile string              `json:"passwordFile"`
}
type backupSetupResponse struct {
	Config   localConfig `json:"config"`
	Snapshot string      `json:"snapshot,omitempty"`
}

func setupClient(c localConfig, target, binary string) (backup.Client, error) {
	if target == "cloud" {
		return configuredCloud(c, binary)
	}
	return configuredBackup(c, binary)
}

// Secrets survive interrupted setup. Never overwrite or delete an existing key.
func setupSecret(path string, data []byte) error {
	if _, err := os.Lstat(path); err == nil {
		return errors.New("此备份位置已有本机钥匙记录，请使用已保存的设置或选择新的位置")
	} else if !os.IsNotExist(err) {
		return err
	}
	return atomicfile.WriteNew(path, func(w io.Writer) error { _, err := w.Write(data); return err })
}
func setupDirectory(path string, c localConfig) error {
	if err := outsideDirectories(path, c.Data, c.BackupRepository); err != nil {
		return err
	}
	if err := os.Mkdir(path, 0700); err != nil && !os.IsExist(err) {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
		return errors.New("本机备份钥匙目录必须是仅当前用户可访问的目录")
	}
	return nil
}
func localSetupState(repository string) (string, error) {
	if _, err := os.Stat(filepath.Join(repository, "config")); err == nil {
		return "ready", nil
	} else if !os.IsNotExist(err) {
		return "", err
	}
	parent, err := os.Stat(filepath.Dir(repository))
	if err != nil || !parent.IsDir() {
		return "", errors.New("备份磁盘不可用，请先连接磁盘")
	}
	entries, err := os.ReadDir(repository)
	if os.IsNotExist(err) || (err == nil && len(entries) == 0) {
		return "empty", nil
	}
	return "", errors.New("备份位置非空，请选择新的空目录或连接已有备份")
}
func prepareBackupSetup(ctx context.Context, path, binary string, c localConfig, l *library.Library, req backupSetupRequest) (localConfig, error) {
	if req.Mode == "current" {
		client, err := setupClient(c, req.Target, binary)
		if err != nil {
			return c, err
		}
		if client.Repository == "" {
			return c, errors.New("尚未配置此备份位置")
		}
		_, err = client.RepositoryID(ctx)
		return c, err // Never initialize a missing previously configured repository.
	}
	if req.Mode != "new" && req.Mode != "existing" {
		return c, errors.New("请选择新建备份或连接已有备份")
	}
	next := c
	if req.Target == "local" {
		next.BackupRepository = filepath.Clean(req.Repository)
	} else {
		next.Cloud = req.Cloud
		next.Cloud.Enabled = true
	}
	location := next.BackupRepository
	if req.Target == "cloud" {
		location = next.Cloud.Connection.Repository()
	}
	dir := filepath.Join(filepath.Dir(path), "backup-secrets")
	keyPath := filepath.Join(dir, req.Target+"-"+digest([]byte(location))+".key")
	if req.Target == "local" {
		next.BackupPasswordFile = keyPath
	} else {
		next.Cloud.PasswordFile = keyPath
		if req.Credentials != nil {
			next.Cloud.CredentialsFile = filepath.Join(dir, "cloud-"+library.NewID()+".json")
		}
	}
	if req.Mode == "new" {
		oldLocation, oldKey := c.BackupRepository, c.BackupPasswordFile
		if req.Target == "cloud" {
			oldLocation, oldKey = c.Cloud.Connection.Repository(), c.Cloud.PasswordFile
		}
		if location == oldLocation && oldKey != "" && oldKey != keyPath {
			return c, errors.New("此位置已有备份配置，请使用已保存的设置；不会更换原来的加密钥匙")
		}
	}
	if err := next.validate(); err != nil {
		return c, err
	}
	if err := setupDirectory(dir, next); err != nil {
		return c, err
	}
	if req.Target == "cloud" && req.Credentials != nil {
		if err := req.Credentials.Validate(); err != nil {
			return c, err
		}
		data, err := json.Marshal(req.Credentials)
		if err != nil {
			return c, err
		}
		defer clear(data)
		if err = setupSecret(next.Cloud.CredentialsFile, data); err != nil {
			return c, err
		}
	}
	_, keyErr := os.Lstat(keyPath)
	if keyErr != nil && !os.IsNotExist(keyErr) {
		return c, keyErr
	}
	if os.IsNotExist(keyErr) {
		var password []byte
		if req.Mode == "new" {
			secret := make([]byte, 32)
			if _, err := rand.Read(secret); err != nil {
				return c, err
			}
			password = []byte(base64.RawURLEncoding.EncodeToString(secret))
			clear(secret)
		} else {
			if req.Password != "" && req.PasswordFile != "" {
				return c, errors.New("请选择一种已有备份解锁方式")
			}
			password = []byte(req.Password)
			if req.PasswordFile != "" {
				if err := outsideDirectories(req.PasswordFile, c.Data, next.BackupRepository); err != nil {
					return c, err
				}
				var err error
				password, err = privateFile(req.PasswordFile, 4096)
				if err != nil {
					return c, err
				}
			}
			password = []byte(strings.TrimRight(string(password), "\r\n"))
			if len(password) < 12 || len(password) > 4096 {
				return c, errors.New("请提供原备份的独立加密密码或旧版口令文件；不是登录密码或保险库口令")
			}
		}
		defer clear(password)
		if err := setupSecret(keyPath, password); err != nil {
			return c, err
		}
	} else if req.Mode == "existing" && (req.Password != "" || req.PasswordFile != "") {
		// A failed imported password must not leave the user unable to correct it.
		// Use a fresh immutable key file; the previous file is retained.
		keyPath = filepath.Join(dir, req.Target+"-"+library.NewID()+".key")
		if req.Password != "" && req.PasswordFile != "" {
			return c, errors.New("请选择一种已有备份解锁方式")
		}
		data := []byte(req.Password)
		if req.PasswordFile != "" {
			if err := outsideDirectories(req.PasswordFile, c.Data, next.BackupRepository); err != nil {
				return c, err
			}
			var err error
			data, err = privateFile(req.PasswordFile, 4096)
			if err != nil {
				return c, err
			}
		}
		defer clear(data)
		if err := setupSecret(keyPath, data); err != nil {
			return c, err
		}
		if req.Target == "local" {
			next.BackupPasswordFile = keyPath
		} else {
			next.Cloud.PasswordFile = keyPath
		}
	}
	client, err := setupClient(next, req.Target, binary)
	if err != nil {
		return c, err
	}
	var state string
	if client.S3 != nil {
		state, err = client.ProbeCloud(ctx)
	} else {
		state, err = localSetupState(client.Repository)
	}
	if err != nil {
		return c, err
	}
	if state == "empty" {
		if req.Mode != "new" {
			return c, errors.New("没有找到已有备份，未创建或覆盖任何仓库")
		}
		// Once initialization has been attempted, a missing repository is ambiguous.
		// Fail closed instead of silently replacing a lost drive/repository.
		attempted := keyPath + ".initializing"
		if err = atomicfile.WriteNew(attempted, func(w io.Writer) error { _, e := w.Write([]byte("initialization attempted\n")); return e }); err != nil {
			return c, errors.New("此位置曾尝试创建备份但仓库当前不可读取。请检查磁盘或云端；不会重新创建仓库，可另选新位置")
		}
		if err = client.Init(ctx); err != nil {
			return c, errors.New("备份仓库创建未完成，本机钥匙已保留。请检查目标后重试，不要删除钥匙目录")
		}
	}
	if _, err = client.RepositoryID(ctx); err != nil {
		return c, err
	}
	if err = ctx.Err(); err != nil {
		return c, err
	}
	// Record the initialized destination before publishing its configuration.
	setting := "backup"
	if req.Target == "cloud" {
		setting = "backup-cloud"
	}
	old, err := l.Setting(setting)
	if err != nil {
		return c, err
	}
	var record map[string]any
	if len(old) > 0 && json.Unmarshal(old, &record) != nil {
		return c, errors.New("备份状态损坏，未覆盖配置")
	}
	if record == nil || record["repository"] != digest([]byte(client.Repository)) {
		record = map[string]any{"repository": digest([]byte(client.Repository)), "initialized": true, "status": "pending", "readVerified": false}
	} else {
		record["initialized"] = true
	}
	data, err := json.Marshal(record)
	if err != nil {
		return c, err
	}
	if err = l.SetSetting(setting, data); err != nil {
		return c, err
	}
	if err = writeConfig(path, next); err != nil {
		return c, errors.New("备份配置保存失败，本机钥匙与仓库已保留，请重试")
	}
	return next, nil
}

func checkSetupBackup(ctx context.Context, c localConfig, l *library.Library, binary, target string) (string, error) {
	client, err := setupClient(c, target, binary)
	if err != nil {
		return "", err
	}
	if _, err = client.RepositoryID(ctx); err != nil {
		return "", err
	}
	stage := filepath.Join(l.Root, "staging", "setup-"+library.NewID())
	defer os.RemoveAll(stage)
	if err = l.Snapshot(ctx, stage); err != nil {
		return "", err
	}
	pinned, err := library.Open(stage)
	if err != nil {
		return "", err
	}
	err = pinned.Verify(ctx)
	closeErr := pinned.Close()
	if err != nil {
		return "", err
	}
	if closeErr != nil {
		return "", closeErr
	}
	id, err := client.BackupLibrary(ctx, stage)
	if err != nil {
		return "", err
	}
	if err = client.Check(ctx); err != nil {
		return "", errors.New("备份尚未通过完整读取校验，请检查连接后重试")
	}
	now := time.Now().UTC()
	data, _ := json.Marshal(map[string]any{"repository": digest([]byte(client.Repository)), "initialized": true, "status": "passed", "attemptedAt": now, "finishedAt": now, "savedAt": now, "verifiedAt": now, "snapshot": id, "readVerified": true})
	setting := "backup"
	if target == "cloud" {
		setting = "backup-cloud"
	}
	if err = l.SetSetting(setting, data); err != nil {
		return "", err
	}
	return id, nil
}

func backupSetupManager(ctx context.Context, action, path, binary string, input io.Reader) error {
	var req backupSetupRequest
	d := json.NewDecoder(io.LimitReader(input, 65537))
	d.DisallowUnknownFields()
	if d.Decode(&req) != nil || d.Decode(&struct{}{}) != io.EOF || (req.Target != "local" && req.Target != "cloud") {
		return errors.New("备份设置请求无效")
	}
	c, saved, err := loadConfig(path)
	if err != nil {
		return err
	}
	if !saved {
		return errors.New("请先完成并保存资料库的初始设置")
	}
	if serviceLoaded(ctx, path) {
		return errors.New("请先停止服务，再设置备份")
	}
	l, err := library.Open(c.Data)
	if err != nil {
		return errors.New("资料库正在使用或不可读取，请先停止服务")
	}
	defer l.Close()
	ctx, cancel := context.WithTimeout(ctx, 24*time.Hour)
	defer cancel()
	out := backupSetupResponse{Config: c}
	if action == "backup-setup" {
		out.Config, err = prepareBackupSetup(ctx, path, binary, c, l, req)
	} else {
		out.Snapshot, err = checkSetupBackup(ctx, c, l, binary, req.Target)
	}
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(out)
}
