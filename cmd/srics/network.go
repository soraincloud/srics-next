package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strings"
	"syscall"
	"time"
)

type serviceAuthStatus struct {
	Configured       bool `json:"configured"`
	VaultConfigured  bool `json:"vaultConfigured"`
	VaultIdleMinutes int  `json:"vaultIdleMinutes"`
}

// Process lifetime alone does not establish that the library is reachable.
// The deadline covers both the connection and the response body.
func probeManagedService(ctx context.Context, url string, transport http.RoundTripper) (serviceAuthStatus, error) {
	var auth serviceAuthStatus
	client := &http.Client{Timeout: time.Second, Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url+"/api/auth", nil)
	if err != nil {
		return auth, err
	}
	res, err := client.Do(req)
	if err != nil {
		return auth, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return auth, fmt.Errorf("服务状态检查返回 HTTP %d", res.StatusCode)
	}
	decoder := json.NewDecoder(io.LimitReader(res.Body, 4097))
	if err := decoder.Decode(&auth); err != nil {
		return auth, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return auth, errors.New("服务状态响应包含多余内容")
	}
	if !auth.Configured {
		return auth, errors.New("服务尚未完成登录配置")
	}
	return auth, nil
}

func localLANAddresses() ([]string, error) {
	addresses, err := net.InterfaceAddrs()
	if err != nil {
		return nil, err
	}
	unique := map[string]bool{}
	for _, address := range addresses {
		ip, _, err := net.ParseCIDR(address.String())
		if err == nil && ip.IsPrivate() {
			unique[ip.String()] = true
		}
	}
	result := make([]string, 0, len(unique))
	for address := range unique {
		result = append(result, address)
	}
	sort.Strings(result)
	return result, nil
}

func checkLANAddress(address string, available []string) error {
	if address == "" {
		return nil
	}
	for _, current := range available {
		if current == address {
			return nil
		}
	}
	message := fmt.Sprintf("局域网地址 %s 已不在这台 Mac 上，IP 可能已变化或网络已断开。", address)
	if len(available) > 0 {
		message += "当前可用地址：" + strings.Join(available, "、") + "。"
	}
	return &fieldError{Field: "lanAddress", Message: message + "请先停止服务，再刷新并重新选择“访问范围”，保存后启动。"}
}

func listenError(address string, err error) error {
	switch {
	case errors.Is(err, syscall.EADDRNOTAVAIL):
		return &fieldError{Field: "lanAddress", Message: "无法使用服务地址 " + address + "：该 IP 已不在本机上。请检查网络，刷新并重新选择“访问范围”。"}
	case errors.Is(err, syscall.EADDRINUSE):
		return &fieldError{Field: "port", Message: "服务地址 " + address + " 的端口已被占用，请停止占用它的服务或更换端口。"}
	default:
		return fmt.Errorf("无法监听服务地址 %s：%w", address, err)
	}
}
