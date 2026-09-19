package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"

	"github.com/soraincloud/srics-next/internal/verification"
)

func TestLocalBoundary(t *testing.T) {
	s := New(context.Background(), "127.0.0.1:19473", fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("hello")}}, nil)
	for _, tc := range []struct {
		name, host, origin, method, path, header string
		status                                   int
	}{
		{"normal", "localhost:19473", "", "GET", "/api/status", "", 200},
		{"rebind", "attacker.example:19473", "", "GET", "/api/status", "", 403},
		{"cross origin", "127.0.0.1:19473", "https://attacker.example", "GET", "/api/status", "", 403},
		{"simple post", "127.0.0.1:19473", "", "POST", "/api/verification", "", 403},
		{"mutation via get", "127.0.0.1:19473", "", "GET", "/api/verification", "", 405},
		{"no data route", "127.0.0.1:19473", "", "GET", "/data/identity.age", "", 404},
		{"no upload route", "127.0.0.1:19473", "", "POST", "/api/upload", "", 405},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, "http://"+tc.host+tc.path, nil)
			req.Header.Set("Origin", tc.origin)
			req.Header.Set("X-SRICS-Request", tc.header)
			w := httptest.NewRecorder()
			s.ServeHTTP(w, req)
			if w.Code != tc.status {
				t.Fatalf("status %d: %s", w.Code, w.Body.String())
			}
			if w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("response may be cached")
			}
		})
	}
}
func TestOnlyOneVerificationAtATime(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := New(ctx, "127.0.0.1:19473", fstest.MapFS{}, func(ctx context.Context, publish func(verification.Report)) verification.Report {
		close(started)
		<-release
		r := verification.Pending()
		r.Status = "passed"
		return r
	})
	request := func() int {
		req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:19473/api/verification", nil)
		req.Header.Set("Origin", "http://127.0.0.1:19473")
		req.Header.Set("X-SRICS-Request", "verification")
		w := httptest.NewRecorder()
		s.ServeHTTP(w, req)
		return w.Code
	}
	if got := request(); got != 202 {
		t.Fatalf("start %d", got)
	}
	<-started
	if got := request(); got != 409 {
		t.Fatalf("duplicate %d", got)
	}
	close(release)
	s.Wait()
}
