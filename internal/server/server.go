// Package server serves the local library and the separate synthetic verification workbench.
package server

import (
	"context"
	"encoding/json"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/soraincloud/srics-next/internal/buildinfo"
	"github.com/soraincloud/srics-next/internal/verification"
)

type Runner func(context.Context, func(verification.Report)) verification.Report
type Server struct {
	ctx     context.Context
	mu      sync.RWMutex
	report  verification.Report
	active  bool
	run     Runner
	allowed map[string]bool
	files   fs.FS
	wg      sync.WaitGroup
	library *LibraryAPI
}

func New(ctx context.Context, address string, files fs.FS, run Runner) *Server {
	host, port, _ := net.SplitHostPort(address)
	s := &Server{ctx: ctx, report: verification.Pending(), run: run, files: files, allowed: map[string]bool{}}
	for _, host := range []string{"127.0.0.1", "localhost", "::1"} {
		s.allowed[net.JoinHostPort(host, port)] = true
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsPrivate() {
		s.allowed[address] = true
	}
	return s
}
func (s *Server) Wait() {
	s.wg.Wait()
	if s.library != nil {
		s.library.wg.Wait()
	}
}
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	limitBodyTime(w, r)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; connect-src 'self'; object-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
	if !s.allowed[r.Host] {
		http.Error(w, "invalid host", http.StatusForbidden)
		return
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		if err != nil || u.Scheme != scheme || u.Host != r.Host || u.Path != "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			http.Error(w, "invalid origin", http.StatusForbidden)
			return
		}
	}
	if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		http.Error(w, "cross-site request rejected", http.StatusForbidden)
		return
	}
	if s.library != nil && strings.HasPrefix(r.URL.Path, "/api/") {
		if !s.library.auth(w, r) {
			return
		}
		if r.URL.Path != "/api/status" && r.URL.Path != "/api/verification" {
			s.library.handle(w, r)
			return
		}
	}
	switch r.URL.Path {
	case "/api/status":
		if r.Method != http.MethodGet {
			methodNotAllowed(w, "GET")
			return
		}
		s.mu.RLock()
		defer s.mu.RUnlock()
		writeJSON(w, http.StatusOK, map[string]any{"report": s.report, "release": buildinfo.Current()})
	case "/api/verification":
		if r.Method != http.MethodPost {
			methodNotAllowed(w, "POST")
			return
		}
		if r.Header.Get("X-SRICS-Request") != "verification" || r.ContentLength != 0 {
			http.Error(w, "invalid verification request", http.StatusForbidden)
			return
		}
		s.mu.Lock()
		if s.active {
			s.mu.Unlock()
			writeJSON(w, http.StatusConflict, map[string]string{"error": "验证正在运行"})
			return
		}
		if s.ctx.Err() != nil {
			s.mu.Unlock()
			http.Error(w, "shutting down", http.StatusServiceUnavailable)
			return
		}
		s.active = true
		s.report = verification.Pending()
		s.report.Status = "running"
		s.report.StartedAt = time.Now().UTC()
		s.wg.Add(1)
		s.mu.Unlock()
		go func() {
			defer s.wg.Done()
			ctx, cancel := context.WithTimeout(s.ctx, 5*time.Minute)
			defer cancel()
			report := s.run(ctx, func(r verification.Report) { s.mu.Lock(); s.report = r; s.mu.Unlock() })
			s.mu.Lock()
			s.report = report
			s.active = false
			s.mu.Unlock()
		}()
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "running"})
	default:
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			methodNotAllowed(w, "GET, HEAD")
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			http.NotFound(w, r)
			return
		}
		if r.URL.Path != "/" && path.Clean(r.URL.Path) != r.URL.Path {
			http.NotFound(w, r)
			return
		}
		legalFiles := map[string]bool{
			"/legal/LICENSE.txt": true, "/legal/NOTICE.txt": true,
			"/legal/THIRD_PARTY_NOTICES.txt": true, "/legal/source-info.json": true,
			"/legal/source.tar.gz": true,
		}
		if r.URL.Path != "/" && !strings.HasPrefix(r.URL.Path, "/assets/") && !legalFiles[r.URL.Path] {
			http.NotFound(w, r)
			return
		}
		if r.URL.Path == "/" {
			if _, err := fs.Stat(s.files, "index.html"); err != nil {
				http.Error(w, "Run ./scripts/build.sh to build the web interface.", http.StatusServiceUnavailable)
				return
			}
		}
		if r.URL.Path == "/legal/source.tar.gz" {
			w.Header().Set("Content-Disposition", `attachment; filename="SRICS-Next-source.tar.gz"`)
			w.Header().Set("Content-Type", "application/gzip")
		}
		http.FileServer(http.FS(s.files)).ServeHTTP(w, r)
	}
}
func methodNotAllowed(w http.ResponseWriter, allow string) {
	w.Header().Set("Allow", allow)
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
