package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"filippo.io/age"
	"fmt"
	"github.com/soraincloud/srics-next/internal/backup"
	"github.com/soraincloud/srics-next/internal/library"
	"github.com/soraincloud/srics-next/internal/media"
	"github.com/soraincloud/srics-next/internal/vault"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"
)

func TestChunkHTTPAuthenticationSessionIsolationAndLock(t *testing.T) {
	root := filepath.Join(t.TempDir(), "library")
	if err := library.Create(root); err != nil {
		t.Fatal(err)
	}
	l, err := library.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := httptest.NewUnstartedServer(nil)
	s := New(ctx, h.Listener.Addr().String(), fstest.MapFS{"index.html": {Data: []byte("app")}}, nil)
	s.EnableLibrary(l, media.Converter{}, backup.Client{})
	h.Config.Handler = s
	h.Start()
	defer h.Close()
	key, _ := age.GenerateX25519Identity()
	a := vault.NewAccess(ctx, key, time.Hour)
	defer a.Lock()
	s.library.sessions["one"] = session{expires: time.Now().Add(time.Hour), csrf: "token", vault: a}
	s.library.sessions["two"] = session{expires: time.Now().Add(time.Hour), csrf: "token2"}
	call := func(session, csrf, method, path string, body []byte) (int, []byte) {
		t.Helper()
		r, _ := http.NewRequest(method, h.URL+path, bytes.NewReader(body))
		r.Header.Set("X-SRICS-Request", "app")
		r.Header.Set("X-SRICS-CSRF", csrf)
		if session != "" {
			r.AddCookie(&http.Cookie{Name: "srics_session", Value: session})
		}
		res, e := h.Client().Do(r)
		if e != nil {
			t.Fatal(e)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, b
	}
	id := library.NewID()
	body := []byte("test-only private chunks")
	manifest, _ := json.Marshal(library.Transfer{ID: id, Module: "files", Name: "test.txt", Size: int64(len(body)), Hashes: []string{fmt.Sprintf("%x", sha256.Sum256(body))}})
	for _, path := range []string{"/api/transfers", "/api/vault/transfers", "/api/storage"} {
		if code, _ := call("", "", "GET", path, nil); code != 401 {
			t.Fatal("anonymous access", path, code)
		}
	}
	if code, _ := call("one", "wrong", "POST", "/api/vault/transfers", manifest); code != 403 {
		t.Fatal("CSRF accepted", code)
	}
	if code, _ := call("two", "token2", "POST", "/api/vault/transfers", manifest); code != 423 {
		t.Fatal("locked create", code)
	}
	if code, b := call("one", "token", "POST", "/api/vault/transfers", manifest); code != 200 {
		t.Fatal(code, string(b))
	}
	if code, _ := call("one", "token", "PUT", "/api/transfers/"+id+"/chunks/0", body); code == 200 {
		t.Fatal("namespace crossing")
	}
	if code, b := call("one", "token", "PUT", "/api/vault/transfers/"+id+"/chunks/0", body); code != 200 {
		t.Fatal(code, string(b))
	}
	if code, b := call("one", "token", "GET", "/api/transfers", nil); code != 200 || bytes.Contains(b, []byte(id)) {
		t.Fatal("private tasks leaked")
	}
	a.Lock()
	if code, _ := call("one", "token", "POST", "/api/vault/transfers/"+id+"/finish", nil); code != 423 {
		t.Fatal("locked finish", code)
	}
	a = vault.NewAccess(ctx, key, time.Hour)
	defer a.Lock()
	s.library.sessions["one"] = session{expires: time.Now().Add(time.Hour), csrf: "token", vault: a}
	if code, b := call("one", "token", "POST", "/api/vault/transfers/"+id+"/finish", nil); code != 200 {
		t.Fatal(code, string(b))
	}
	if code, b := call("one", "token", "GET", "/api/vault/items/"+id+"/download", nil); code != 200 || !bytes.Equal(b, body) {
		t.Fatal("resumed download", code, string(b))
	}
}
