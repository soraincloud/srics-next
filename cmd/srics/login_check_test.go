package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"

	"github.com/soraincloud/srics-next/internal/backup"
	"github.com/soraincloud/srics-next/internal/library"
	"github.com/soraincloud/srics-next/internal/media"
	"github.com/soraincloud/srics-next/internal/server"
)

func TestNewPasswordsRejectBrowserStrippedNewlines(t *testing.T) {
	for _, field := range []string{"password", "vaultPassword"} {
		for _, newline := range []string{"\r", "\n", "\r\n"} {
			root := t.TempDir()
			req := configureRequest{Config: localConfig{Data: filepath.Join(root, "library"), Port: 19473}, Password: "synthetic-login-password"}
			if field == "password" {
				req.Password += newline
			} else {
				req.VaultPassword = "synthetic-vault" + newline + "password"
			}
			var issue *fieldError
			if err := applyConfig(filepath.Join(root, "config.json"), req); !errors.As(err, &issue) || issue.Field != field {
				t.Fatal("newline password was accepted or attributed to the wrong field")
			}
		}
	}
}

func TestFreshConfigurationLoginRoundTrip(t *testing.T) {
	for _, password := range []string{"synthetic-login-123", "测试登录-password-123", " synthetic-\"cafe\u0301\"-🔑-123 "} {
		t.Run(strconv.Itoa(len(password)), func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "config.json")
			h := httptest.NewUnstartedServer(nil)
			_, port, _ := net.SplitHostPort(h.Listener.Addr().String())
			n, _ := strconv.Atoi(port)
			c := localConfig{Data: filepath.Join(root, "library"), Port: n}
			if err := applyConfig(path, configureRequest{Config: c, Password: password, VaultPassword: "different-private-password", VaultIdleMinutes: 10}); err != nil {
				t.Fatal(err)
			}
			l, err := library.Open(c.Data)
			if err != nil {
				t.Fatal(err)
			}
			defer l.Close()
			s := server.New(context.Background(), h.Listener.Addr().String(), fstest.MapFS{}, nil)
			s.EnableLibrary(l, media.Converter{}, backup.Client{})
			closedSessions := make(chan []*http.Cookie, 1)
			h.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/auth/logout" {
					closedSessions <- r.Cookies()
				}
				s.ServeHTTP(w, r)
			})
			h.Start()
			defer h.Close()
			if err := verifyStartedLogin(context.Background(), path, c, password); err != nil {
				t.Fatal(err)
			}
			var cookies []*http.Cookie
			select {
			case cookies = <-closedSessions:
			default:
			}
			if len(cookies) == 0 {
				t.Fatal("verification session was not explicitly closed")
			}
			req, _ := http.NewRequest("GET", h.URL+"/api/auth", nil)
			for _, cookie := range cookies {
				req.AddCookie(cookie)
			}
			response, err := h.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			var auth struct {
				Authenticated bool `json:"authenticated"`
			}
			if json.NewDecoder(response.Body).Decode(&auth) != nil || auth.Authenticated {
				t.Fatal("verification left a live login session")
			}
			if err := verifyStartedLogin(context.Background(), path, c, "different-private-password"); err == nil || strings.Contains(err.Error(), password) {
				t.Fatal("vault password accepted for login or error exposed password")
			}
		})
	}
}

func TestLoginVerificationDoesNotFollowRedirects(t *testing.T) {
	var forwarded atomic.Bool
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { forwarded.Store(true); w.WriteHeader(200) }))
	defer other.Close()
	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusTemporaryRedirect)
	}))
	defer h.Close()
	_, port, _ := net.SplitHostPort(h.Listener.Addr().String())
	n, _ := strconv.Atoi(port)
	if err := verifyStartedLogin(context.Background(), "", localConfig{Port: n}, "synthetic-login-123"); err == nil {
		t.Fatal("redirect accepted")
	}
	if forwarded.Load() {
		t.Fatal("login credentials followed a redirect")
	}
}
