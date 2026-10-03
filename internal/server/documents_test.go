package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/soraincloud/srics-next/internal/backup"
	"github.com/soraincloud/srics-next/internal/library"
	"github.com/soraincloud/srics-next/internal/media"
	"golang.org/x/crypto/bcrypt"
)

func TestDocumentHTTPAccessSearchOriginalDownloadAndTrash(t *testing.T) {
	root := filepath.Join(t.TempDir(), "library")
	if err := library.Create(root); err != nil {
		t.Fatal(err)
	}
	l, err := library.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	hash, _ := bcrypt.GenerateFromPassword([]byte("synthetic-document-login"), bcrypt.MinCost)
	if err = l.Setup(hash); err != nil {
		t.Fatal(err)
	}
	h := httptest.NewUnstartedServer(nil)
	s := New(context.Background(), h.Listener.Addr().String(), fstest.MapFS{}, nil)
	s.EnableLibrary(l, media.Converter{}, backup.Client{})
	h.Config.Handler = s
	h.Start()
	defer h.Close()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	csrf := ""
	request := func(method, path string, body any, want int) ([]byte, http.Header) {
		t.Helper()
		encoded, _ := json.Marshal(body)
		req, _ := http.NewRequest(method, h.URL+path, bytes.NewReader(encoded))
		req.Header.Set("X-SRICS-Request", "app")
		req.Header.Set("X-SRICS-CSRF", csrf)
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		out, err := io.ReadAll(res.Body)
		if err != nil || res.StatusCode != want {
			t.Fatalf("%s %s: %d %s %v", method, path, res.StatusCode, out, err)
		}
		if res.Header.Get("Cache-Control") != "no-store" {
			t.Fatal("document response cacheable")
		}
		return out, res.Header
	}
	id, raw := library.NewID(), "# Night 夜航\r\n\r\n<script>原文不应修改</script>  \r\n"
	base := "/api/documents/" + id
	item := "/api/items/" + id
	body := map[string]any{"id": id, "name": "Night 夜航", "tags": []string{"笔记", "工作"}, "body": raw}
	request("GET", base, nil, 401)
	request("POST", "/api/documents", body, 401)
	auth, _ := request("POST", "/api/auth/login", map[string]string{"password": "synthetic-document-login"}, 200)
	request("POST", "/api/documents", body, 403)
	var state struct{ CSRF string }
	if err = json.Unmarshal(auth, &state); err != nil {
		t.Fatal(err)
	}
	csrf = state.CSRF
	request("POST", "/api/documents", body, 200)
	request("POST", "/api/documents", body, 200)
	list := func(query string, count int) {
		t.Helper()
		out, _ := request("GET", "/api/library?module=documents"+query, nil, 200)
		var data struct {
			Items []library.Item
			Tags  []string
		}
		if err := json.Unmarshal(out, &data); err != nil || len(data.Items) != count {
			t.Fatal("document filter", string(out), err)
		}
	}
	list("&q=night&tag=笔记&tag=工作", 1)
	list("&tag=笔记&tag=missing", 0)
	list("&q=other", 0)
	out, header := request("GET", item+"/download", nil, 200)
	if string(out) != raw || !strings.Contains(header.Get("Content-Disposition"), ".md") || header.Get("Content-Type") != "text/markdown; charset=utf-8" {
		t.Fatal("Markdown download changed original", header, string(out))
	}
	request("HEAD", item+"/download", nil, 200)
	body["body"], body["revision"] = "", 1
	request("PUT", base, body, 200)
	request("PUT", base, body, 200)
	body["body"] = "stale edit"
	request("PUT", base, body, 409)
	body["body"], body["revision"] = strings.Repeat("x", library.MaxDocumentBody+1), 2
	request("PUT", base, body, 400)
	out, _ = request("GET", item+"/download", nil, 200)
	if len(out) != 0 {
		t.Fatal("empty Markdown download changed")
	}
	request("GET", "/api/download?id="+id, nil, 400)
	request("DELETE", item, nil, 200)
	request("GET", base, nil, 404)
	request("POST", "/api/documents", map[string]any{"id": id, "name": "reuse", "body": ""}, 409)
	request("POST", item+"/restore", nil, 200)
	request("GET", base, nil, 200)
	request("POST", "/api/auth/logout", nil, 200)
	request("GET", base, nil, 401)
	request("GET", item+"/download", nil, 401)
}
