package server

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/soraincloud/srics-next/internal/backup"
	"github.com/soraincloud/srics-next/internal/library"
	"github.com/soraincloud/srics-next/internal/media"
	"golang.org/x/crypto/bcrypt"
)

func TestLibraryHTTPAuthenticationAndFlow(t *testing.T) {
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
	s := New(context.Background(), h.Listener.Addr().String(), fstest.MapFS{"index.html": {Data: []byte("app")}}, nil)
	cwebp, _ := exec.LookPath("cwebp")
	s.EnableLibrary(l, media.Converter{CWebP: cwebp}, backup.Client{})
	h.Config.Handler = s
	h.Start()
	defer h.Close()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	csrf := ""
	call := func(method, path string, body []byte, headers map[string]string) (int, []byte) {
		t.Helper()
		req, _ := http.NewRequest(method, h.URL+path, bytes.NewReader(body))
		req.Header.Set("X-SRICS-Request", "app")
		if csrf != "" {
			req.Header.Set("X-SRICS-CSRF", csrf)
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		res, e := client.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		defer res.Body.Close()
		b, e := io.ReadAll(res.Body)
		if e != nil {
			t.Fatal(e)
		}
		return res.StatusCode, b
	}
	for _, path := range []string{"/api/library?module=images", "/api/uploads", "/api/backup", "/api/items/00000000000000000000000000000000/download", "/api/status"} {
		if code, _ := call("GET", path, nil, nil); code != 401 {
			t.Fatal("unauthenticated route", path, code)
		}
	}
	if code, _ := call("POST", "/api/auth/setup", []byte(`{"password":"synthetic-password-for-tests"}`), nil); code != 404 {
		t.Fatal("HTTP password creation must be unavailable", code)
	}
	if hash, _ := l.Setting("password"); len(hash) != 0 {
		t.Fatal("HTTP setup wrote a password")
	}
	loginHash, err := bcrypt.GenerateFromPassword([]byte("synthetic-password-for-tests"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Setup(loginHash); err != nil {
		t.Fatal(err)
	}
	code, data := call("POST", "/api/auth/login", []byte(`{"password":"synthetic-password-for-tests"}`), nil)
	if code != 200 {
		t.Fatal(code, string(data))
	}
	var auth struct {
		CSRF string `json:"csrf"`
	}
	json.Unmarshal(data, &auth)
	csrf = auth.CSRF
	if code, _ = call("POST", "/api/auth/setup", []byte(`{"password":"replacement-password"}`), nil); code != 404 {
		t.Fatal("setup replaced existing login")
	}
	if code, _ = call("POST", "/api/uploads", []byte(`{}`), map[string]string{"X-SRICS-CSRF": ""}); code != 403 {
		t.Fatal("missing csrf accepted")
	}
	if code, _ = call("GET", "/api/library?module=images", nil, map[string]string{"Origin": "https://evil.example"}); code != 403 {
		t.Fatal("cross-origin allowed")
	}
	var b bytes.Buffer
	png.Encode(&b, image.NewNRGBA(image.Rect(0, 0, 4, 4)))
	raw := b.Bytes()
	hash := sha256.Sum256(raw)
	digest := hex.EncodeToString(hash[:])
	put := func(module, name string) library.Item {
		t.Helper()
		manifest := library.Upload{ID: library.NewID(), Module: module, Name: name, Tags: []string{"测试", "标签"}, Files: []library.UploadFile{{Name: "2.png", Size: int64(len(raw))}}}
		if module != "comics" {
			manifest.Tags = nil
		}
		payload, _ := json.Marshal(manifest)
		code, data := call("POST", "/api/uploads", payload, nil)
		if code != 200 {
			t.Fatal(code, string(data))
		}
		code, data = call("PUT", "/api/uploads/"+manifest.ID+"/files/0", raw, map[string]string{"X-File-SHA256": digest})
		if code != 200 {
			t.Fatal(code, string(data))
		}
		code, data = call("POST", "/api/uploads/"+manifest.ID+"/finish", nil, nil)
		if code != 200 {
			t.Fatal(code, string(data))
		}
		var it library.Item
		json.Unmarshal(data, &it)
		return it
	}
	imageItem := put("images", "2.png")
	_ = put("photos", "2.png")
	code, data = call("GET", "/api/items/"+imageItem.ID+"/download", nil, nil)
	if code != 200 || !bytes.Equal(data, raw) {
		t.Fatal("original download changed")
	}
	code, data = call("GET", "/api/library?module=images&random=1", nil, nil)
	var listing struct {
		Items []library.Item `json:"items"`
	}
	json.Unmarshal(data, &listing)
	if code != 200 || len(listing.Items) != 1 || listing.Items[0].Module != "images" {
		t.Fatal("random pool leaked another module")
	}
	code, _ = call("DELETE", "/api/items/"+imageItem.ID, nil, nil)
	if code != 200 {
		t.Fatal(code)
	}
	code, _ = call("GET", "/api/items/"+imageItem.ID+"/pages/0", nil, nil)
	if code != 404 {
		t.Fatal("deleted image still served")
	}
	code, _ = call("POST", "/api/items/"+imageItem.ID+"/restore", nil, nil)
	if code != 200 {
		t.Fatal("restore failed")
	}
	if cwebp != "" {
		comic := put("comics", "我的漫画")
		code, data = call("GET", "/api/library?module=comics&q="+"不存在", nil, nil)
		json.Unmarshal(data, &listing)
		if code != 200 || len(listing.Items) != 0 {
			t.Fatal("name search ignored")
		}
		code, data = call("GET", "/api/items/"+comic.ID+"/download", nil, nil)
		if code != 200 {
			t.Fatal(code)
		}
		zr, e := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if e != nil || len(zr.File) != 1 || !strings.HasSuffix(zr.File[0].Name, "/00001.webp") {
			t.Fatal("invalid comic archive", e)
		}
		f, _ := zr.File[0].Open()
		pageBytes, _ := io.ReadAll(f)
		f.Close()
		sum := sha256.Sum256(pageBytes)
		if hex.EncodeToString(sum[:]) != comic.Pages[0].SHA256 {
			t.Fatal("archive did not use source object")
		}
	}
	code, _ = call("POST", "/api/auth/logout", nil, nil)
	if code != 200 {
		t.Fatal(code)
	}
	code, _ = call("GET", "/api/library?module=images", nil, nil)
	if code != 401 {
		t.Fatal("logout did not invalidate session")
	}
	code, data = call("POST", "/api/auth/login", []byte(`{"password":"synthetic-password-for-tests"}`), nil)
	if code != 200 {
		t.Fatal("login after restart", string(data))
	}
}
func TestRandomPaginationStableUnderDeletion(t *testing.T) {
	root := filepath.Join(t.TempDir(), "library")
	if err := library.Create(root); err != nil {
		t.Fatal(err)
	}
	l, err := library.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	a := LibraryAPI{store: l}
	var b bytes.Buffer
	png.Encode(&b, image.NewNRGBA(image.Rect(0, 0, 1, 1)))
	raw := b.Bytes()
	sum := sha256.Sum256(raw)
	hash := hex.EncodeToString(sum[:])
	add := func() string {
		up, e := l.CreateUpload(library.Upload{ID: library.NewID(), Module: "images", Name: "same.png", Files: []library.UploadFile{{Name: "same.png", Size: int64(len(raw))}}})
		if e != nil {
			t.Fatal(e)
		}
		if _, e = l.Receive(context.Background(), up.ID, 0, hash, bytes.NewReader(raw), media.Converter{}); e != nil {
			t.Fatal(e)
		}
		if _, e = l.Finish(up.ID); e != nil {
			t.Fatal(e)
		}
		return up.ID
	}
	for i := 0; i < 45; i++ {
		add()
	}
	req := httptest.NewRequest("GET", "/api/library?module=images&random=1", nil)
	out, err := a.list(req)
	if err != nil {
		t.Fatal(err)
	}
	first := out.(map[string]any)
	page := first["items"].([]library.Item)
	if len(page) != 40 {
		t.Fatal("expected first page of 40")
	}
	seen := map[string]bool{}
	for _, it := range page {
		seen[it.ID] = true
	}
	if err = l.Trash(page[0].ID, false); err != nil {
		t.Fatal(err)
	}
	newID := add()
	req = httptest.NewRequest("GET", "/api/library?module=images&random=1&seed="+first["seed"].(string)+"&after="+first["next"].(string)+"&snapshot=45", nil)
	out, err = a.list(req)
	if err != nil {
		t.Fatal(err)
	}
	second := out.(map[string]any)["items"].([]library.Item)
	if len(second) != 5 {
		t.Fatal("missing items after deletion", len(second))
	}
	for _, it := range second {
		if seen[it.ID] || it.ID == newID {
			t.Fatal("repeated or newly added item in existing random round")
		}
	}
}
