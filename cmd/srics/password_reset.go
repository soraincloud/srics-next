package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/soraincloud/srics-next/internal/library"
	"golang.org/x/crypto/bcrypt"
)

// Login reset is a local owner operation over stdin, never an HTTP endpoint.
// Vault changes use configure with the current vault password. Recovery JSON
// belongs only to backup recovery and is not accepted by this request.
type localPasswordReset struct {
	Password string `json:"password"`
}

func (r localPasswordReset) validate() error {
	if strings.ContainsAny(r.Password, "\r\n") || utf8.RuneCountInString(r.Password) < 12 || len(r.Password) > 72 {
		return &fieldError{Field: "password", Message: "新登录密码至少 12 个字符，最多 72 字节，不能包含换行符"}
	}
	return nil
}
func resetLocalPasswords(ctx context.Context, l *library.Library, r localPasswordReset) error {
	if err := r.validate(); err != nil {
		return err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(r.Password), 12)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return l.SetSettings(map[string][]byte{"password": hash})
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
