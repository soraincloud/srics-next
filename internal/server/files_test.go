package server

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"filippo.io/age"
	"github.com/soraincloud/srics-next/internal/backup"
	"github.com/soraincloud/srics-next/internal/library"
	"github.com/soraincloud/srics-next/internal/media"
	"github.com/soraincloud/srics-next/internal/vault"
)

func TestOrdinaryFileHTTPWithoutVaultSearchDownloadAndTrash(t *testing.T) {
	root := filepath.Join(t.TempDir(), "library")
	if err := library.Create(root); err != nil {
		t.Fatal(err)
	}
	l, err := library.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	h := httptest.NewUnstartedServer(nil)
	s := New(context.Background(), h.Listener.Addr().String(), fstest.MapFS{}, nil)
	s.EnableLibrary(l, media.Converter{}, backup.Client{})
	h.Config.Handler = s
	h.Start()
	defer h.Close()
	// A logged-in session with no unlocked vault must be sufficient.
	s.library.sessions["file-test"] = session{expires: time.Now().Add(time.Hour), csrf: "file-csrf"}
	request := func(method, path string, body any, authenticated bool, token string, want int) ([]byte, http.Header) {
		t.Helper()
		var data []byte
		if raw, ok := body.([]byte); ok {
			data = raw
		} else if body != nil {
			data, _ = json.Marshal(body)
		}
		r, _ := http.NewRequest(method, h.URL+path, bytes.NewReader(data))
		r.Header.Set("X-SRICS-Request", "app")
		r.Header.Set("X-SRICS-CSRF", token)
		if authenticated {
			r.AddCookie(&http.Cookie{Name: "srics_session", Value: "file-test"})
		}
		res, e := h.Client().Do(r)
		if e != nil {
			t.Fatal(e)
		}
		defer res.Body.Close()
		out, e := io.ReadAll(res.Body)
		if e != nil || res.StatusCode != want {
			t.Fatalf("%s %s: %d %s %v", method, path, res.StatusCode, out, e)
		}
		return out, res.Header
	}
	request("GET", "/api/library?module=attachments", nil, false, "", 401)
	ids := []string{}
	original := []byte("<script>alert('original must only download')</script>")
	for i, name := range []string{"Night 资料", "其他文件", "隐藏文件"} {
		id := library.NewID()
		filename := "same.html"
		if i == 2 {
			filename = ".example-config"
		}
		manifest := library.Upload{ID: id, Module: "attachments", Name: name, Files: []library.UploadFile{{Name: filename, Size: int64(len(original))}}}
		request("POST", "/api/uploads", manifest, false, "", 401)
		request("POST", "/api/uploads", manifest, true, "wrong", 403)
		request("POST", "/api/uploads", manifest, true, "file-csrf", 200)
		tr := library.Transfer{ID: library.NewID(), Module: "attachments", Parent: id, Name: filename, Size: int64(len(original)), Hashes: []string{fmt.Sprintf("%x", sha256.Sum256(original))}}
		request("POST", "/api/transfers", tr, true, "file-csrf", 200)
		request("PUT", "/api/transfers/"+tr.ID+"/chunks/0", original, true, "file-csrf", 200)
		request("POST", "/api/transfers/"+tr.ID+"/finish", nil, true, "file-csrf", 200)
		request("POST", "/api/uploads/"+id+"/finish", nil, true, "file-csrf", 200)
		ids = append(ids, id)
	}
	list, _ := request("GET", "/api/library?module=attachments&q=nIgHt", nil, true, "", 200)
	var data struct{ Items []library.Item }
	if err = json.Unmarshal(list, &data); err != nil || len(data.Items) != 1 || data.Items[0].ID != ids[0] {
		t.Fatal("name search failed", string(list), err)
	}
	base := "/api/items/" + ids[0]
	request("GET", base+"/download", nil, false, "", 401)
	out, headers := request("GET", base+"/download", nil, true, "", 200)
	if !bytes.Equal(out, original) || headers.Get("Content-Type") != "application/octet-stream" || !strings.HasPrefix(headers.Get("Content-Disposition"), "attachment;") {
		t.Fatal("unsafe or changed original download", headers)
	}
	request("GET", base+"/pages/0", nil, true, "", 415)
	request("PATCH", base, map[string]any{"name": "改名", "tags": []string{}, "revision": 1}, true, "file-csrf", 200)
	request("PATCH", base, map[string]any{"name": "过期", "revision": 1}, true, "file-csrf", 409)
	hidden, hiddenHeaders := request("GET", "/api/items/"+ids[2]+"/download", nil, true, "", 200)
	if !bytes.Equal(hidden, original) || !strings.Contains(hiddenHeaders.Get("Content-Disposition"), ".example-config") {
		t.Fatal("hidden file name changed", hiddenHeaders)
	}
	zipped, _ := request("GET", "/api/download?id="+ids[0]+"&id="+ids[1]+"&id="+ids[2], nil, true, "", 200)
	z, err := zip.NewReader(bytes.NewReader(zipped), int64(len(zipped)))
	if err != nil || len(z.File) != 3 || z.File[0].Name == z.File[1].Name || z.File[2].Name != ".example-config" {
		t.Fatal("batch names collided", err)
	}
	for _, f := range z.File {
		r, e := f.Open()
		if e != nil {
			t.Fatal(e)
		}
		b, e := io.ReadAll(r)
		r.Close()
		if e != nil || !bytes.Equal(b, original) {
			t.Fatal("batch original changed", e)
		}
	}
	// A logged-in client must not reach private files through the ordinary
	// namespace, even with a known private identifier or filename.
	key, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	access := vault.NewAccess(context.Background(), key, time.Hour)
	secret := []byte("synthetic-private-file-boundary")
	private, err := l.ReceivePrivate(context.Background(), access, library.NewID(), "files", "private-boundary.txt", int64(len(secret)), bytes.NewReader(secret))
	access.Lock()
	if err != nil {
		t.Fatal(err)
	}
	request("GET", "/api/items/"+private.ID, nil, true, "", 404)
	request("GET", "/api/items/"+private.ID+"/download", nil, true, "", 404)
	request("GET", "/api/download?id="+ids[0]+"&id="+private.ID, nil, true, "", 400)
	request("GET", "/api/vault/items/"+private.ID+"/download", nil, true, "", 423)
	for _, path := range []string{"/api/library?module=attachments", "/api/library?module=all&trash=1", "/api/uploads", "/api/transfers", "/api/library/stats"} {
		body, _ := request("GET", path, nil, true, "", 200)
		if bytes.Contains(body, []byte(private.ID)) || bytes.Contains(body, []byte(private.Name)) || bytes.Contains(body, secret) {
			t.Fatal("private information leaked", path)
		}
	}
	request("DELETE", base, nil, true, "file-csrf", 200)
	request("GET", base+"/download", nil, true, "", 404)
	request("POST", base+"/restore", nil, true, "file-csrf", 200)
	out, _ = request("GET", base+"/download", nil, true, "", 200)
	if !bytes.Equal(out, original) {
		t.Fatal("restored file changed")
	}
	request("DELETE", base, nil, true, "file-csrf", 200)
	request("POST", base+"/purge", nil, true, "file-csrf", 200)
	request("GET", base+"/download", nil, true, "", 404)
}
