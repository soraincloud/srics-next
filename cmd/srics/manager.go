package main

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/json"
	"encoding/xml"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/soraincloud/srics-next/internal/buildinfo"
	"github.com/soraincloud/srics-next/internal/library"
	"github.com/soraincloud/srics-next/internal/localtls"
	"github.com/soraincloud/srics-next/internal/verification"
	"golang.org/x/sys/unix"
)

type managerStatus struct {
	Version          string         `json:"version"`
	Release          buildinfo.Info `json:"release"`
	UpdateBackup     string         `json:"updateBackup"`
	Config           localConfig    `json:"config"`
	Saved            bool           `json:"saved"`
	Running          bool           `json:"running"`
	PasswordSet      bool           `json:"passwordSet"`
	LoginVerified    bool           `json:"loginVerified"`
	VaultSet         bool           `json:"vaultSet"`
	VaultIdleMinutes int            `json:"vaultIdleMinutes"`
	URL              string         `json:"url"`
	Log              string         `json:"log"`
	LANAddresses     []string       `json:"lanAddresses"`
	Certificate      string         `json:"certificate"`
	CertFingerprint  string         `json:"certFingerprint"`
	NetworkError     string         `json:"networkError"`
	DataError        string         `json:"dataError"`
	CloudCheck       string         `json:"cloudCheck"`
}

func serviceLabel(path string) string {
	return fmt.Sprintf("com.soraincloud.srics.%x", sha256.Sum256([]byte(path)))
}
func serviceTarget(path string) string {
	return fmt.Sprintf("gui/%d/%s", os.Getuid(), serviceLabel(path))
}
func serviceLoaded(ctx context.Context, path string) bool {
	return exec.CommandContext(ctx, "/bin/launchctl", "print", serviceTarget(path)).Run() == nil
}
func readManagerStatus(ctx context.Context, path string) (managerStatus, error) {
	c, saved, err := loadConfig(path)
	s := managerStatus{Version: version, Release: buildinfo.Current(), Config: c, Saved: saved, VaultIdleMinutes: 10, URL: c.url(), Log: filepath.Join(filepath.Dir(path), "service.log"), LANAddresses: []string{}}
	if err != nil {
		return s, err
	}
	addresses, _ := net.InterfaceAddrs()
	for _, address := range addresses {
		ip, _, err := net.ParseCIDR(address.String())
		if err == nil && ip.IsPrivate() {
			s.LANAddresses = append(s.LANAddresses, ip.String())
		}
	}
	transport := &http.Transport{Proxy: nil}
	defer transport.CloseIdleConnections()
	if c.LANAddress != "" {
		roots, fingerprint, err := localtls.Authority(tlsDirectory(path))
		if err != nil {
			s.NetworkError = "HTTPS 证书不可用，请检查配置目录，或停止服务后切回仅本机访问"
		} else {
			s.Certificate, s.CertFingerprint = filepath.Join(tlsDirectory(path), localtls.PublicFile), fingerprint
			transport.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS13}
		}
	}
	if runtime.GOOS == "darwin" {
		out, _ := exec.CommandContext(ctx, "/bin/launchctl", "print", serviceTarget(path)).Output()
		s.Running = strings.Contains(string(out), "state = running")
	}
	if s.Running {
		if s.NetworkError != "" {
			return s, nil
		}
		client := &http.Client{Timeout: time.Second, Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		res, err := client.Get(s.URL + "/api/auth")
		if err == nil {
			defer res.Body.Close()
			var auth struct {
				Configured       bool `json:"configured"`
				VaultConfigured  bool `json:"vaultConfigured"`
				VaultIdleMinutes int  `json:"vaultIdleMinutes"`
			}
			if res.StatusCode == 200 && json.NewDecoder(io.LimitReader(res.Body, 4096)).Decode(&auth) == nil {
				s.PasswordSet = auth.Configured
				s.VaultSet = auth.VaultConfigured
				s.VaultIdleMinutes = auth.VaultIdleMinutes
			}
		}
	} else if l, err := library.Open(c.Data); err == nil {
		hash, err := l.Setting("password")
		wrapped, _ := l.Setting("vault-key")
		s.VaultSet = len(wrapped) > 0
		idle, _ := l.Setting("vault-idle")
		if n, e := strconv.Atoi(string(idle)); e == nil && n >= 1 && n <= 60 {
			s.VaultIdleMinutes = n
		}
		l.Close()
		s.PasswordSet = err == nil && len(hash) > 0
	} else if _, statErr := os.Lstat(c.Data); saved || !os.IsNotExist(statErr) {
		s.DataError = err.Error()
	}
	return s, nil
}
func xmlText(s string) string {
	var b strings.Builder
	xml.EscapeText(&b, []byte(s))
	return b.String()
}
func launchPlist(path, executable string, s managerStatus) string {
	restart := ""
	if s.Config.AutoRestart {
		restart = "<key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict><key>ThrottleInterval</key><integer>30</integer>"
	}
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>Label</key><string>%s</string>
<key>ProgramArguments</key><array><string>%s</string><string>serve</string><string>--config</string><string>%s</string></array>
<key>RunAtLoad</key><true/>
%s
<key>ExitTimeOut</key><integer>120</integer>
<key>Umask</key><integer>63</integer>
<key>StandardOutPath</key><string>%s</string>
<key>StandardErrorPath</key><string>%s</string>
</dict></plist>`, xmlText(serviceLabel(path)), xmlText(executable), xmlText(path), restart, xmlText(s.Log), xmlText(s.Log))
}
func startManaged(ctx context.Context, path string) error {
	if runtime.GOOS != "darwin" {
		return errors.New("后台启动当前仅支持 macOS")
	}
	s, err := readManagerStatus(ctx, path)
	if err != nil {
		return err
	}
	if s.Running && s.PasswordSet {
		return nil
	}
	if s.DataError != "" {
		return errors.New(s.DataError)
	}
	if !s.Saved || !s.PasswordSet {
		return errors.New("请先保存本机配置和登录密码")
	}
	if _, err := configuredBackup(s.Config, ""); err != nil {
		return err
	}
	if _, err := configuredCloud(s.Config, ""); err != nil {
		return err
	}
	if s.Config.LANAddress != "" {
		if err := localtls.Ensure(tlsDirectory(path), s.Config.LANAddress); err != nil {
			return err
		}
	}
	dep := verification.ResolveTools()
	if dep.CWebP == "" || dep.Restic == "" {
		return errors.New("缺少 cwebp 或 restic，请重新构建完整程序包")
	}
	listener, err := net.Listen("tcp", s.Config.address())
	if err != nil {
		return errors.New("端口已占用，请停止旧服务或更换端口")
	}
	listener.Close()
	if serviceLoaded(ctx, path) {
		if err := stopManaged(ctx, path); err != nil {
			return err
		}
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if err = syncLoginAgent(path, s.Config, exe); err != nil {
		return err
	}
	plist := filepath.Join(filepath.Dir(path), "service.plist")
	if err = os.WriteFile(plist, []byte(launchPlist(path, exe, s)), 0600); err != nil {
		return err
	}
	log, err := os.OpenFile(s.Log, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	log.Close()
	if err = exec.CommandContext(ctx, "/bin/launchctl", "bootstrap", fmt.Sprintf("gui/%d", os.Getuid()), plist).Run(); err != nil {
		return fmt.Errorf("无法注册后台服务：%w", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		current, err := readManagerStatus(ctx, path)
		if err == nil && current.Running && current.PasswordSet {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
	_ = stopManaged(ctx, path)
	return errors.New("服务启动失败，请查看运行日志")
}
func stopManaged(ctx context.Context, path string) error {
	if runtime.GOOS != "darwin" {
		return errors.New("后台管理当前仅支持 macOS")
	}
	if !serviceLoaded(ctx, path) {
		return nil
	}
	if err := exec.CommandContext(ctx, "/bin/launchctl", "bootout", serviceTarget(path)).Run(); err != nil {
		return fmt.Errorf("停止失败：%w", err)
	}
	c, _, err := loadConfig(path)
	if err != nil {
		return err
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		l, err := library.Open(c.Data)
		if err == nil {
			l.Close()
			return nil
		}
		if _, err := os.Stat(c.Data); os.IsNotExist(err) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(150 * time.Millisecond):
		}
	}
	return errors.New("服务仍在完成任务，请稍后刷新状态")
}
func manager(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: srics manager [info|configure|start|stop|migrate-library|reset-passwords|cloud-check|recovery-snapshots|recovery-restore|recovery-inspect|recovery-activate|recovery-items|recovery-export|prepare-update] [--config path]")
	}
	path, err := configPath()
	if err != nil {
		return err
	}
	flags := flag.NewFlagSet("manager", flag.ContinueOnError)
	flags.StringVar(&path, "config", path, "local configuration file")
	verifyLogin := flags.Bool("verify-login", false, "verify the password from stdin after start")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 || !filepath.IsAbs(path) {
		return errors.New("配置文件需要绝对路径")
	}
	if *verifyLogin && args[0] != "start" {
		return errors.New("--verify-login 仅用于 start")
	}
	path = filepath.Clean(path)
	if args[0] != "info" {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return err
		}
		f, err := os.OpenFile(filepath.Join(filepath.Dir(path), ".manager.lock"), os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			return err
		}
		defer f.Close()
		if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
			return errors.New("另一项本机配置操作正在进行")
		}
		defer unix.Flock(int(f.Fd()), unix.LOCK_UN)
	}
	switch args[0] {
	case "info":
	case "prepare-update":
		updateCtx, cancel := context.WithTimeout(ctx, 24*time.Hour)
		defer cancel()
		dir, e := prepareUpdate(updateCtx, path)
		if e != nil {
			return e
		}
		status, e := readManagerStatus(ctx, path)
		if e != nil {
			return e
		}
		status.UpdateBackup = dir
		return json.NewEncoder(os.Stdout).Encode(status)
	case "recovery-key-status", "recovery-key-generate", "recovery-key-confirm":
		return recoveryKeyManager(ctx, args[0], path, verification.ResolveTools().Restic, os.Stdin)
	case "recovery-snapshots", "recovery-restore", "recovery-activate", "recovery-inspect", "recovery-items", "recovery-export", "recovery-reset-passwords":
		return recoveryManager(ctx, args[0], path, verification.ResolveTools().Restic, os.Stdin)
	case "cloud-check":
		var request struct {
			Config localConfig `json:"config"`
		}
		d := json.NewDecoder(io.LimitReader(os.Stdin, 65537))
		d.DisallowUnknownFields()
		if d.Decode(&request) != nil || d.Decode(&struct{}{}) != io.EOF {
			return errors.New("云端配置格式不正确")
		}
		message, err := checkCloud(ctx, request.Config, verification.ResolveTools().Restic)
		if err != nil {
			return err
		}
		status, err := readManagerStatus(ctx, path)
		if err != nil {
			return err
		}
		status.CloudCheck = message
		return json.NewEncoder(os.Stdout).Encode(status)
	case "migrate-library":
		if err := migrationManager(ctx, path, os.Stdin); err != nil {
			return err
		}
	case "reset-passwords":
		if err := resetLocalPasswordsManager(ctx, path, os.Stdin); err != nil {
			return err
		}
	case "configure":
		if runtime.GOOS == "darwin" && serviceLoaded(ctx, path) {
			current, err := readManagerStatus(ctx, path)
			if err != nil {
				return err
			}
			if current.Running {
				return errors.New("请先停止服务再修改配置")
			}
			if err := stopManaged(ctx, path); err != nil {
				return err
			}
		}
		var req configureRequest
		decoder := json.NewDecoder(io.LimitReader(os.Stdin, 65537))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&req); err != nil {
			return errors.New("本机配置格式不正确")
		}
		if decoder.Decode(&struct{}{}) != io.EOF {
			return errors.New("本机配置包含多余内容")
		}
		if err := applyConfig(path, req); err != nil {
			return err
		}
		exe, e := os.Executable()
		if e != nil {
			return e
		}
		if e = syncLoginAgent(path, req.Config, exe); e != nil {
			return e
		}
	case "start":
		if *verifyLogin {
			var request struct {
				Password string `json:"password"`
			}
			d := json.NewDecoder(io.LimitReader(os.Stdin, 4096))
			d.DisallowUnknownFields()
			if d.Decode(&request) != nil || d.Decode(&struct{}{}) != io.EOF || request.Password == "" || len(request.Password) > 72 {
				return errors.New("启动登录验证需要有效的登录密码")
			}
			if err := startAndVerifyLogin(ctx, path, request.Password); err != nil {
				return err
			}
		} else if err := startManaged(ctx, path); err != nil {
			return err
		}
	case "stop":
		if err := stopManaged(ctx, path); err != nil {
			return err
		}
	default:
		return errors.New("未知本机管理操作")
	}
	status, err := readManagerStatus(ctx, path)
	if err != nil {
		return err
	}
	status.LoginVerified = *verifyLogin
	return json.NewEncoder(os.Stdout).Encode(status)
}
