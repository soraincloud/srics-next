package server

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/soraincloud/srics-next/internal/backup"
	"github.com/soraincloud/srics-next/internal/library"
	"github.com/soraincloud/srics-next/internal/media"
	"golang.org/x/crypto/bcrypt"
)

func integrityLibrary(t *testing.T) *library.Library {
	t.Helper()
	root := filepath.Join(t.TempDir(), "library")
	if err := library.Create(root); err != nil {
		t.Fatal(err)
	}
	l, err := library.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	return l
}

func TestHTTPOriginalAndArchiveRejectSameSizeCorruption(t *testing.T) {
	l := integrityLibrary(t)
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "media", "lossless-bare.webp"))
	if err != nil {
		t.Fatal(err)
	}
	up, err := l.CreateUpload(library.Upload{ID: library.NewID(), Module: "images", Name: "original", Files: []library.UploadFile{{Name: "original.webp", Size: int64(len(data))}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.Receive(context.Background(), up.ID, 0, fmt.Sprintf("%x", sha256.Sum256(data)), bytes.NewReader(data), media.Converter{}); err != nil {
		t.Fatal(err)
	}
	it, err := l.Finish(up.ID)
	if err != nil {
		t.Fatal(err)
	}
	h := httptest.NewUnstartedServer(nil)
	s := New(context.Background(), h.Listener.Addr().String(), nil, nil)
	s.EnableLibrary(l, media.Converter{}, backup.Client{})
	s.library.sessions["test"] = session{expires: time.Now().Add(time.Hour)}
	h.Config.Handler = s
	h.Start()
	defer h.Close()
	get := func(path, rangeHeader string) (*http.Response, error) {
		r, _ := http.NewRequest("GET", h.URL+path, nil)
		r.AddCookie(&http.Cookie{Name: "srics_session", Value: "test"})
		r.Header.Set("Range", rangeHeader)
		return h.Client().Do(r)
	}
	for _, path := range []string{"/api/items/" + it.ID + "/download", "/api/items/" + it.ID + "/pages/0"} {
		res, err := get(path, "bytes=0-9")
		if err != nil {
			t.Fatal(err)
		}
		got, readErr := io.ReadAll(res.Body)
		res.Body.Close()
		if readErr != nil || res.StatusCode != 206 || !bytes.Equal(got, data[:10]) {
			t.Fatal("valid range failed", res.StatusCode, readErr)
		}
	}
	bad := bytes.Clone(data)
	bad[len(bad)-1] ^= 1 // Outside the requested range: the whole original must be checked.
	if err := os.WriteFile(l.ObjectPath(it.Pages[0].Object), bad, 0600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/items/" + it.ID + "/download", "/api/items/" + it.ID + "/pages/0"} {
		res, err := get(path, "bytes=0-9")
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != 503 {
			t.Fatal("corrupt original served", res.StatusCode)
		}
	}
	res, err := get("/api/download?id="+it.ID, "")
	if err == nil {
		body, readErr := io.ReadAll(res.Body)
		res.Body.Close()
		_, zipErr := zip.NewReader(bytes.NewReader(body), int64(len(body)))
		if res.StatusCode == 200 && readErr == nil && zipErr == nil {
			t.Fatal("corrupt ZIP completed successfully")
		}
		if res.StatusCode == 404 {
			t.Fatal("test used nonexistent batch route")
		}
	}
}

type notifiedBody struct {
	io.ReadCloser
	started chan struct{}
	once    bool
}

func (b *notifiedBody) Read(p []byte) (int, error) {
	if !b.once {
		close(b.started)
		b.once = true
	}
	return b.ReadCloser.Read(p)
}

func TestIncompleteLoginDoesNotBlockAnotherLogin(t *testing.T) {
	l := integrityLibrary(t)
	hash, err := bcrypt.GenerateFromPassword([]byte("synthetic-audit-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Setup(hash); err != nil {
		t.Fatal(err)
	}
	a := &LibraryAPI{store: l, sessions: map[string]session{}}
	reader, writer := io.Pipe()
	defer writer.Close()
	defer reader.Close()
	started := make(chan struct{})
	slow := httptest.NewRequest("POST", "/api/auth/login", nil)
	slow.Header.Set("X-SRICS-Request", "app")
	slow.Body = &notifiedBody{ReadCloser: reader, started: started}
	done := make(chan struct{})
	go func() { defer close(done); a.auth(httptest.NewRecorder(), slow) }()
	<-started
	fast := httptest.NewRequest("POST", "/api/auth/login", strings.NewReader(`{"password":"synthetic-audit-password"}`))
	fast.Header.Set("X-SRICS-Request", "app")
	response := httptest.NewRecorder()
	finished := make(chan struct{})
	go func() { defer close(finished); a.auth(response, fast) }()
	select {
	case <-finished:
		if response.Code != 200 {
			t.Fatal("valid login failed", response.Code)
		}
	case <-time.After(2 * time.Second):
		writer.Close()
		<-done
		<-finished
		t.Fatal("incomplete request retained global login lock")
	}
	writer.Close()
	<-done
}

func TestBodyReadDeadlineInterruptsStalledSocket(t *testing.T) {
	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = &timedBody{ReadCloser: r.Body, ctx: r.Context(), controller: http.NewResponseController(w), end: time.Now().Add(200 * time.Millisecond), idle: time.Second}
		if _, err := io.ReadAll(r.Body); err == nil {
			w.WriteHeader(200)
		} else {
			w.WriteHeader(408)
		}
	}))
	defer h.Close()
	conn, err := net.Dial("tcp", h.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(3 * time.Second))
	fmt.Fprintf(conn, "POST / HTTP/1.1\r\nHost: %s\r\nContent-Length: 100\r\n\r\nx", h.Listener.Addr())
	res, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: "POST"})
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 408 {
		t.Fatal("stalled body not timed out", res.StatusCode)
	}
}

type deadlineResponse struct {
	*httptest.ResponseRecorder
	deadline time.Time
}

func (w *deadlineResponse) SetReadDeadline(deadline time.Time) error {
	w.deadline = deadline
	return nil
}

func TestRejectedRequestBodyHasDeadlineBeforeReading(t *testing.T) {
	s := New(context.Background(), "127.0.0.1:19473", nil, nil)
	r := httptest.NewRequest("POST", "http://invalid.example/api/auth/login", strings.NewReader("unfinished"))
	w := &deadlineResponse{ResponseRecorder: httptest.NewRecorder()}
	s.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden || w.deadline.IsZero() || time.Until(w.deadline) > 15*time.Second {
		t.Fatal("rejected unread body can outlive the login deadline", w.Code, w.deadline)
	}
}
