package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/soraincloud/srics-next/internal/backup"
	"github.com/soraincloud/srics-next/internal/library"
	"github.com/soraincloud/srics-next/internal/media"
)

func TestSourceDownloadsDoNotBypassLibraryAuthentication(t *testing.T) {
	root := t.TempDir() + "/library"
	if err := library.Create(root); err != nil {
		t.Fatal(err)
	}
	l, err := library.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	files := fstest.MapFS{
		"legal/LICENSE.txt":             {Data: []byte("synthetic license")},
		"legal/NOTICE.txt":              {Data: []byte("synthetic notice")},
		"legal/THIRD_PARTY_NOTICES.txt": {Data: []byte("synthetic dependency notices")},
		"legal/source-info.json":        {Data: []byte(`{"project":"SRICS Next"}`)},
		"legal/source.tar.gz":           {Data: []byte("synthetic source")},
		"legal/config.json":             {Data: []byte("must not be accessible")},
	}
	s := New(context.Background(), "127.0.0.1:19473", files, nil)
	s.EnableLibrary(l, media.Converter{}, backup.Client{})
	for _, path := range []string{"/legal/LICENSE.txt", "/legal/NOTICE.txt", "/legal/THIRD_PARTY_NOTICES.txt", "/legal/source-info.json", "/legal/source.tar.gz"} {
		r := httptest.NewRequest("GET", "http://127.0.0.1:19473"+path, nil)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != 200 || w.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Fatalf("%s: %d", path, w.Code)
		}
		if path == "/legal/source.tar.gz" && !strings.HasPrefix(w.Header().Get("Content-Disposition"), "attachment;") {
			t.Fatal("source is not a download")
		}
	}
	for _, tc := range []struct {
		method, path, host string
		status             int
	}{
		{"GET", "/api/library/stats", "127.0.0.1:19473", http.StatusUnauthorized},
		{"GET", "/api/status", "127.0.0.1:19473", http.StatusUnauthorized},
		{"GET", "/legal/config.json", "127.0.0.1:19473", http.StatusNotFound},
		{"GET", "/legal/", "127.0.0.1:19473", http.StatusNotFound},
		{"GET", "/assets/../legal/config.json", "127.0.0.1:19473", http.StatusNotFound},
		{"GET", "/data/identity.age", "127.0.0.1:19473", http.StatusNotFound},
		{"POST", "/legal/source.tar.gz", "127.0.0.1:19473", http.StatusMethodNotAllowed},
		{"GET", "/legal/source.tar.gz", "attacker.example:19473", http.StatusForbidden},
	} {
		r := httptest.NewRequest(tc.method, "http://"+tc.host+tc.path, nil)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatalf("%s %s: %d, want %d", tc.method, tc.path, w.Code, tc.status)
		}
	}
}
