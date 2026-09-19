package server

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"testing"
	"testing/fstest"
	"time"

	"filippo.io/age"
	"github.com/soraincloud/srics-next/internal/backup"
	"github.com/soraincloud/srics-next/internal/library"
	"github.com/soraincloud/srics-next/internal/media"
	"github.com/soraincloud/srics-next/internal/vault"
)

func TestPrivateHTTPIsolationLockDownloadAndSearch(t *testing.T) {
	root := filepath.Join(t.TempDir(), "library")
	if e := library.Create(root); e != nil {
		t.Fatal(e)
	}
	l, e := library.Open(root)
	if e != nil {
		t.Fatal(e)
	}
	defer l.Close()
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := httptest.NewUnstartedServer(nil)
	s := New(parent, h.Listener.Addr().String(), fstest.MapFS{"index.html": {Data: []byte("app")}}, nil)
	s.EnableLibrary(l, media.Converter{}, backup.Client{})
	h.Config.Handler = s
	h.Start()
	defer h.Close()
	key, _ := age.GenerateX25519Identity()
	access := vault.NewAccess(parent, key, time.Hour)
	s.library.sessions["first"] = session{expires: time.Now().Add(time.Hour), csrf: "csrf-first", vault: access}
	s.library.sessions["second"] = session{expires: time.Now().Add(time.Hour), csrf: "csrf-second"}
	call := func(token, method, path string, body []byte, headers map[string]string) (int, []byte) {
		t.Helper()
		req, _ := http.NewRequest(method, h.URL+path, bytes.NewReader(body))
		req.AddCookie(&http.Cookie{Name: "srics_session", Value: token})
		req.Header.Set("X-SRICS-CSRF", "csrf-"+token)
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		res, e := h.Client().Do(req)
		if e != nil {
			t.Fatal(e)
		}
		defer res.Body.Close()
		b, e := io.ReadAll(res.Body)
		if e != nil {
			t.Fatal(e)
		}
		if res.Header.Get("Cache-Control") != "no-store" {
			t.Fatal("private caching allowed")
		}
		return res.StatusCode, b
	}
	id := library.NewID()
	raw := []byte("synthetic sensitive body")
	code, b := call("first", "PUT", "/api/vault/items/"+id, raw, map[string]string{"X-File-Name": url.PathEscape("秘密文件.txt"), "X-File-Size": "999", "X-File-Module": "files"})
	// Actual size is derived below to avoid trusting a declared manifest.
	if code == 200 {
		t.Fatal("incorrect upload size accepted")
	}
	code, b = call("first", "PUT", "/api/vault/items/"+id, raw, map[string]string{"X-File-Name": url.PathEscape("秘密文件.txt"), "X-File-Size": fmtSize(len(raw)), "X-File-Module": "files"})
	if code != 200 {
		t.Fatal(code, string(b))
	}
	var item library.PrivateItem
	json.Unmarshal(b, &item)
	for _, route := range []struct{ method, path, body string }{{"POST", "/api/vault/list", `{"module":"files"}`}, {"GET", "/api/vault/items/" + id + "/download", ""}, {"GET", "/api/vault/items/" + id + "/preview", ""}, {"GET", "/api/vault/download?id=" + id, ""}, {"PATCH", "/api/vault/items/" + id, `{"action":"trash","revision":1}`}, {"PUT", "/api/vault/items/" + library.NewID(), ""}, {"POST", "/api/vault/activity", ""}} {
		if code, _ = call("second", route.method, route.path, []byte(route.body), nil); code != 423 {
			t.Fatal("other device access", route.path, code)
		}
	}
	code, b = call("first", "POST", "/api/vault/list", []byte(`{"module":"files","q":"秘密"}`), nil)
	if code != 200 || !bytes.Contains(b, []byte(id)) {
		t.Fatal("name search failed", code, string(b))
	}
	code, b = call("first", "GET", "/api/library?module=images", nil, nil)
	if code != 200 || bytes.Contains(b, []byte("秘密")) {
		t.Fatal("ordinary leak")
	}
	code, b = call("first", "GET", "/api/vault/items/"+id+"/download", nil, nil)
	if code != 200 || !bytes.Equal(b, raw) {
		t.Fatal("download corrupted")
	}
	code, b = call("first", "GET", "/api/vault/download?id="+id+"&id="+id, nil, nil)
	if code != 200 {
		t.Fatal(code, string(b))
	}
	z, e := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if e != nil || len(z.File) != 2 || z.File[0].Name == z.File[1].Name {
		t.Fatal("batch zip invalid", e)
	}
	for _, f := range z.File {
		r, e := f.Open()
		if e != nil {
			t.Fatal(e)
		}
		data, e := io.ReadAll(r)
		r.Close()
		if e != nil || !bytes.Equal(data, raw) {
			t.Fatal("batch bytes changed", e)
		}
	}
	code, _ = call("first", "POST", "/api/vault/lock", nil, nil)
	if code != 200 {
		t.Fatal(code)
	}
	code, _ = call("first", "GET", "/api/vault/items/"+id+"/download", nil, nil)
	if code != 423 {
		t.Fatal("download after lock", code)
	}
	// A stalled body must be interrupted without waiting for the uploader to send
	// another byte. A second, independent HTTP request performs the lock.
	access = vault.NewAccess(parent, key, time.Hour)
	s.library.mu.Lock()
	record := s.library.sessions["first"]
	record.vault = access
	s.library.sessions["first"] = record
	s.library.mu.Unlock()
	reader, writer := io.Pipe()
	defer writer.Close()
	req, _ := http.NewRequest("PUT", h.URL+"/api/vault/items/"+library.NewID(), reader)
	req.AddCookie(&http.Cookie{Name: "srics_session", Value: "first"})
	req.Header.Set("X-SRICS-CSRF", "csrf-first")
	req.Header.Set("X-File-Name", "blocked.txt")
	req.Header.Set("X-File-Module", "files")
	req.Header.Set("X-File-Size", "200000")
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		res, e := h.Client().Do(req)
		if e == nil {
			io.Copy(io.Discard, res.Body)
			res.Body.Close()
		}
	}()
	if _, e = writer.Write([]byte("partial")); e != nil {
		t.Fatal(e)
	}
	deadline := time.Now().Add(time.Second)
	for len(s.library.private.uploads) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(s.library.private.uploads) == 0 {
		t.Fatal("upload never started")
	}
	call("first", "POST", "/api/vault/lock", nil, nil)
	// Closing the local producer allows net/http's client write goroutine to exit;
	// server cancellation itself must have already released its upload slot.
	deadline = time.Now().Add(time.Second)
	for len(s.library.private.uploads) != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(s.library.private.uploads) != 0 {
		t.Fatal("locked upload kept processing")
	}
	writer.Close()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("upload did not stop")
	}
}
func fmtSize(n int) string { return strconv.Itoa(n) }

func TestPrivateRandomPaginationSnapshotAndModuleIsolation(t *testing.T) {
	root := filepath.Join(t.TempDir(), "library")
	if err := library.Create(root); err != nil {
		t.Fatal(err)
	}
	l, err := library.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	key, _ := age.GenerateX25519Identity()
	access := vault.NewAccess(context.Background(), key, time.Hour)
	defer access.Lock()
	a := &LibraryAPI{store: l}
	var picture bytes.Buffer
	png.Encode(&picture, image.NewNRGBA(image.Rect(0, 0, 2, 2)))
	put := func(module string) library.PrivateItem {
		t.Helper()
		it, e := l.ReceivePrivate(context.Background(), access, library.NewID(), module, "same.png", int64(picture.Len()), bytes.NewReader(picture.Bytes()))
		if e != nil {
			t.Fatal(e)
		}
		return it
	}
	for i := 0; i < 45; i++ {
		put("private")
	}
	put("files")
	result, err := a.privateList(context.Background(), access, privateQuery{Module: "private", Random: true})
	if err != nil {
		t.Fatal(err)
	}
	page := result.(map[string]any)
	first := page["items"].([]library.PrivateItem)
	if len(first) != 40 || page["total"].(int) != 45 {
		t.Fatal("pagination size", page)
	}
	added := put("private")
	seen := map[string]bool{}
	for _, it := range first {
		seen[it.ID] = true
	}
	result, err = a.privateList(context.Background(), access, privateQuery{Module: "private", Random: true, Seed: page["seed"].(string), Snapshot: page["snapshot"].(int64), After: page["next"].(string)})
	if err != nil {
		t.Fatal(err)
	}
	last := result.(map[string]any)["items"].([]library.PrivateItem)
	if len(last) != 5 {
		t.Fatal("new uploads changed an existing round", len(last))
	}
	for _, it := range last {
		if seen[it.ID] || it.Module != "private" || it.ID == added.ID {
			t.Fatal("duplicate or cross-space result")
		}
	}
}
