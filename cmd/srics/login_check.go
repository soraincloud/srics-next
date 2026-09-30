package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/soraincloud/srics-next/internal/localtls"
)

// fieldError is machine-readable by the local launcher, without carrying secrets.
type fieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

func (e *fieldError) Error() string { return e.Message }

func verifyStartedLogin(ctx context.Context, configPath string, c localConfig, password string) error {
	transport := &http.Transport{Proxy: nil}
	defer transport.CloseIdleConnections()
	if c.LANAddress != "" {
		roots, _, err := localtls.Authority(tlsDirectory(configPath))
		if err != nil {
			return errors.New("无法验证局域网 HTTPS 证书")
		}
		transport.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS13}
	}
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	payload, err := json.Marshal(map[string]string{"password": password})
	if err != nil {
		return err
	}
	defer clear(payload)
	req, err := http.NewRequestWithContext(ctx, "POST", c.url()+"/api/auth/login", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("X-SRICS-Request", "app")
	req.Header.Set("Content-Type", "application/json")
	res, err := client.Do(req)
	if err != nil {
		return errors.New("无法连接刚启动的服务完成登录验证")
	}
	defer res.Body.Close()
	var auth struct {
		Authenticated bool   `json:"authenticated"`
		CSRF          string `json:"csrf"`
	}
	if res.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(res.Body, 4096)).Decode(&auth) != nil || !auth.Authenticated || auth.CSRF == "" {
		return errors.New("刚保存的登录密码未通过服务验证")
	}
	// Dispose of the test session; do not transfer cookies to the user's browser.
	logout, err := http.NewRequestWithContext(ctx, "POST", c.url()+"/api/auth/logout", nil)
	if err != nil {
		return err
	}
	logout.Header.Set("X-SRICS-Request", "app")
	logout.Header.Set("X-SRICS-CSRF", auth.CSRF)
	for _, cookie := range res.Cookies() {
		logout.AddCookie(cookie)
	}
	end, err := client.Do(logout)
	if err != nil {
		return errors.New("登录验证完成，但无法结束验证会话")
	}
	defer end.Body.Close()
	if end.StatusCode != http.StatusOK {
		return errors.New("登录验证完成，但验证会话未正常结束")
	}
	return nil
}

func startAndVerifyLogin(ctx context.Context, path, password string) error {
	if err := startManaged(ctx, path); err != nil {
		return err
	}
	c, _, err := loadConfig(path)
	if err == nil {
		err = verifyStartedLogin(ctx, path, c, password)
	}
	if err == nil {
		return nil
	}
	message := "配置已保存，但启动后的登录验证失败：" + err.Error()
	if stopErr := stopManaged(ctx, path); stopErr != nil {
		return fmt.Errorf("%s。服务停止失败，请在本机程序中检查运行状态：%w", message, stopErr)
	}
	return errors.New(message + "。服务已停止，未打开浏览器；请检查登录密码设置和服务地址。")
}
