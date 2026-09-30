package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/soraincloud/srics-next/internal/library"
	"github.com/soraincloud/srics-next/internal/vault"
	"golang.org/x/crypto/bcrypt"
)

// This is a local owner operation over stdin, never an HTTP endpoint. Login
// reset relies on local filesystem authority; vault reset requires its recovery key.
type localPasswordReset struct {
	Password        string `json:"password"`
	VaultPassword   string `json:"vaultPassword"`
	RecoveryKeyFile string `json:"recoveryKeyFile"`
}

func (r localPasswordReset) validate() error {
	if r.Password == "" && r.VaultPassword == "" {
		return errors.New("请选择要重设的密码并填写新密码")
	}
	if r.Password != "" && (strings.ContainsAny(r.Password, "\r\n") || utf8.RuneCountInString(r.Password) < 12 || len(r.Password) > 72) {
		return &fieldError{Field: "password", Message: "新登录密码至少 12 个字符，最多 72 字节，不能包含换行符"}
	}
	if r.VaultPassword != "" {
		if strings.ContainsAny(r.VaultPassword, "\r\n") || len(r.VaultPassword) < 12 || len(r.VaultPassword) > 1024 {
			return &fieldError{Field: "vaultPassword", Message: "新保险库口令需为 12–1024 字节，不能包含换行符"}
		}
		if r.RecoveryKeyFile == "" {
			return &fieldError{Field: "recoveryKeyFile", Message: "请选择此资料库的恢复 JSON"}
		}
	}
	return nil
}
func resetLocalPasswords(ctx context.Context, l *library.Library, r localPasswordReset) error {
	if err := r.validate(); err != nil {
		return err
	}
	values := map[string][]byte{}
	if r.VaultPassword != "" {
		a, err := recoveryKeyAccess(ctx, l, r.RecoveryKeyFile)
		if err != nil {
			return &fieldError{Field: "recoveryKeyFile", Message: err.Error()}
		}
		if a == nil {
			return &fieldError{Field: "recoveryKeyFile", Message: "此资料库尚未设置保险库，无需重设口令"}
		}
		defer a.Lock()
		identity, _, _, err := a.Snapshot()
		if err != nil {
			return err
		}
		values["vault-key"], err = vault.Wrap(identity, r.VaultPassword)
		if err != nil {
			return err
		}
	}
	if r.Password != "" {
		hash, err := bcrypt.GenerateFromPassword([]byte(r.Password), 12)
		if err != nil {
			return err
		}
		values["password"] = hash
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// Both changes commit together, only after recovery validation succeeds.
	return l.SetSettings(values)
}
func resetLocalPasswordsManager(ctx context.Context, path string, input io.Reader) error {
	var r localPasswordReset
	d := json.NewDecoder(io.LimitReader(input, 16385))
	d.DisallowUnknownFields()
	if d.Decode(&r) != nil || d.Decode(&struct{}{}) != io.EOF {
		return errors.New("重设密码请求格式不正确")
	}
	if err := r.validate(); err != nil {
		return err
	}
	c, saved, err := loadConfig(path)
	if err != nil {
		return err
	}
	if !saved {
		return errors.New("请先完成首次配置")
	}
	// Stop the owned service, dropping all login sessions and unlocked vault access.
	// Acquire the library lock before writing; never change a live server's password.
	if err := stopManaged(ctx, path); err != nil {
		return err
	}
	l, err := library.Open(c.Data)
	if err != nil {
		return err
	}
	defer l.Close()
	return resetLocalPasswords(ctx, l, r)
}
