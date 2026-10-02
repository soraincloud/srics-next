package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/soraincloud/srics-next/internal/backup"
)

type cloudConfig struct {
	Enabled         bool            `json:"enabled"`
	Connection      backup.S3Config `json:"connection"`
	CredentialsFile string          `json:"credentialsFile"`
	PasswordFile    string          `json:"passwordFile"`
}

func privateFile(path string, limit int64) ([]byte, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("凭据与口令文件需使用绝对路径")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.New("无法读取凭据或口令文件")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("凭据与口令文件需为仅本人可读的普通文件（权限 600）")
	}
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(b)) > limit {
		clear(b)
		return nil, errors.New("凭据或口令文件无法读取或过大")
	}
	return b, nil
}

func outsideDirectories(file string, directories ...string) error {
	if !filepath.IsAbs(file) {
		return errors.New("凭据与口令文件需使用绝对路径")
	}
	f, err := resolvedPath(file)
	if err != nil {
		return err
	}
	for _, dir := range directories {
		if dir == "" {
			continue
		}
		d, err := resolvedPath(dir)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(d, f)
		if err != nil || rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))) {
			return errors.New("云端凭据和口令必须存放在资料目录、本地备份目录之外")
		}
	}
	return nil
}

func validateCloud(c localConfig) error {
	if !c.Cloud.Enabled {
		return nil
	}
	if err := c.Cloud.Connection.Validate(); err != nil {
		return err
	}
	for _, file := range []string{c.Cloud.CredentialsFile, c.Cloud.PasswordFile} {
		if err := outsideDirectories(file, c.Data, c.BackupRepository); err != nil {
			return err
		}
	}
	credentials, err := resolvedPath(c.Cloud.CredentialsFile)
	if err != nil {
		return err
	}
	for _, password := range []string{c.Cloud.PasswordFile, c.BackupPasswordFile} {
		if password == "" {
			continue
		}
		p, err := resolvedPath(password)
		if err != nil {
			return err
		}
		if p == credentials {
			return errors.New("云账号凭据与备份解密口令不能使用同一个文件")
		}
	}
	if c.Cloud.Connection.CAFile != "" && !filepath.IsAbs(c.Cloud.Connection.CAFile) {
		return errors.New("云端 CA 文件需使用绝对路径")
	}
	return nil
}

func configuredCloud(c localConfig, binary string) (backup.Client, error) {
	if c.Unified != nil {
		return backup.Client{}, nil
	}
	client := backup.Client{Binary: binary}
	if !c.Cloud.Enabled {
		return client, nil
	}
	if err := validateCloud(c); err != nil {
		return client, err
	}
	data, err := privateFile(c.Cloud.CredentialsFile, 32768)
	if err != nil {
		return client, err
	}
	defer clear(data)
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(&client.Credentials) != nil || d.Decode(&struct{}{}) != io.EOF {
		return client, errors.New("云端凭据文件需为有效 JSON：accessKeyId、secretAccessKey、可选 sessionToken")
	}
	if err = client.Credentials.Validate(); err != nil {
		return client, err
	}
	password, err := privateFile(c.Cloud.PasswordFile, 4096)
	if err != nil {
		return client, err
	}
	defer clear(password)
	client.Password = strings.TrimRight(string(password), "\r\n")
	if len(client.Password) < 12 {
		return client, errors.New("云端备份口令至少需要 12 字节")
	}
	connection := c.Cloud.Connection
	client.S3 = &connection
	client.Repository = connection.Repository()
	return client, nil
}

func checkCloud(ctx context.Context, c localConfig, binary string) (string, error) {
	if err := c.validate(); err != nil {
		return "", err
	}
	client, err := configuredCloud(c, binary)
	if err != nil {
		return "", err
	}
	if client.S3 == nil {
		return "", errors.New("请先填写云端备份配置")
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	state, err := client.ProbeCloud(ctx)
	if err != nil {
		return "", err
	}
	if state == "empty" {
		return "连接正常，专用前缀为空；首次备份时初始化加密仓库", nil
	}
	return "连接正常，已有加密仓库可读取；尚需执行备份并单独验证恢复", nil
}
