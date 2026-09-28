package server

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/soraincloud/srics-next/internal/backup"
	"github.com/soraincloud/srics-next/internal/library"
	"github.com/soraincloud/srics-next/internal/media"
)

type observedEOF struct {
	io.Reader
	done chan struct{}
	once sync.Once
}

func (r *observedEOF) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	if err == io.EOF {
		r.once.Do(func() { close(r.done) })
	}
	return n, err
}

func TestLockInvalidatesAlreadyPendingUnlock(t *testing.T) {
	root := filepath.Join(t.TempDir(), "library")
	if err := library.Create(root); err != nil {
		t.Fatal(err)
	}
	l, err := library.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	wrapped, err := l.PrepareVault("synthetic-unlock-password", "")
	if err != nil {
		t.Fatal(err)
	}
	if err = l.SetSetting("vault-key", wrapped); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := New(ctx, "127.0.0.1:19473", fstest.MapFS{}, nil)
	s.EnableLibrary(l, media.Converter{}, backup.Client{})
	s.library.sessions["test"] = session{expires: time.Now().Add(time.Hour), csrf: "test-csrf"}
	request := func(path string, body io.Reader) *http.Request {
		r := httptest.NewRequest("POST", "http://127.0.0.1:19473"+path, body)
		r.AddCookie(&http.Cookie{Name: "srics_session", Value: "test"})
		r.Header.Set("X-SRICS-CSRF", "test-csrf")
		return r
	}
	// Hold the password-work mutex so the unlock is definitely pending when
	// another request locks the session; no timing-dependent sleeps are needed.
	s.library.private.mu.Lock()
	blocked := true
	defer func() {
		if blocked {
			s.library.private.mu.Unlock()
		}
	}()
	body := &observedEOF{Reader: bytes.NewBufferString(`{"password":"synthetic-unlock-password"}`), done: make(chan struct{})}
	unlock := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { defer close(done); s.ServeHTTP(unlock, request("/api/vault/unlock", body)) }()
	select {
	case <-body.done:
	case <-time.After(5 * time.Second):
		t.Fatal("unlock body was not read")
	}
	lock := httptest.NewRecorder()
	s.ServeHTTP(lock, request("/api/vault/lock", nil))
	if lock.Code != 200 {
		t.Fatal("lock failed", lock.Code)
	}
	s.library.private.mu.Unlock()
	blocked = false
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("unlock did not finish")
	}
	saved, ok := s.library.current(request("/api/vault/status", nil))
	if _, _, _, err := saved.vault.Snapshot(); !ok || err == nil || unlock.Code == 200 {
		t.Fatal("an older unlock reopened the explicitly locked session", unlock.Code)
	}
	// A new intentional unlock after the lock still works.
	next := httptest.NewRecorder()
	s.ServeHTTP(next, request("/api/vault/unlock", bytes.NewBufferString(`{"password":"synthetic-unlock-password"}`)))
	if next.Code != 200 {
		t.Fatal("fresh unlock rejected", next.Code)
	}
}
