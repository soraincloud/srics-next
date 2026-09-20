package server

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/soraincloud/srics-next/internal/backup"
	"github.com/soraincloud/srics-next/internal/library"
	"github.com/soraincloud/srics-next/internal/localtls"
	"github.com/soraincloud/srics-next/internal/media"
	"golang.org/x/crypto/bcrypt"
)

func TestHTTPSLoginCookieAndCSRF(t *testing.T) {
	root := t.TempDir()
	data := filepath.Join(root, "library")
	dir := filepath.Join(root, "tls")
	if err := library.Create(data); err != nil {
		t.Fatal(err)
	}
	l, err := library.Open(data)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	hash, _ := bcrypt.GenerateFromPassword([]byte("synthetic-https-password"), bcrypt.MinCost)
	if err = l.Setup(hash); err != nil {
		t.Fatal(err)
	}
	if err = localtls.Ensure(dir, "127.0.0.1"); err != nil {
		t.Fatal(err)
	}
	cert, roots, err := localtls.Load(dir, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	h := httptest.NewUnstartedServer(nil)
	s := New(context.Background(), h.Listener.Addr().String(), fstest.MapFS{}, nil)
	s.EnableLibrary(l, media.Converter{}, backup.Client{})
	h.Config.Handler = s
	h.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13}
	h.StartTLS()
	defer h.Close()
	jar, _ := cookiejar.New(nil)
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS13}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Jar: jar}
	r, _ := http.NewRequest("POST", h.URL+"/api/auth/login", strings.NewReader(`{"password":"synthetic-https-password"}`))
	r.Header.Set("Origin", h.URL)
	r.Header.Set("X-SRICS-Request", "app")
	res, err := client.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	var login struct {
		CSRF string `json:"csrf"`
	}
	if err = json.NewDecoder(res.Body).Decode(&login); err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 200 || login.CSRF == "" {
		t.Fatal("HTTPS login failed", res.StatusCode)
	}
	cookies := res.Cookies()
	if len(cookies) != 1 || !cookies[0].Secure || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatal("insecure HTTPS session cookie")
	}
	for _, token := range []string{"", login.CSRF} {
		r, _ = http.NewRequest("POST", h.URL+"/api/auth/logout", nil)
		r.Header.Set("Origin", h.URL)
		r.Header.Set("X-SRICS-CSRF", token)
		res, err = client.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if token == "" && res.StatusCode != 403 {
			t.Fatal("missing CSRF accepted")
		}
		if token != "" && (res.StatusCode != 200 || !res.Cookies()[0].Secure) {
			t.Fatal("HTTPS logout failed")
		}
	}
}
