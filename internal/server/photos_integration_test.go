//go:build integration

package server

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"image"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/soraincloud/srics-next/internal/backup"
	"github.com/soraincloud/srics-next/internal/library"
	"github.com/soraincloud/srics-next/internal/media"
	"github.com/soraincloud/srics-next/internal/vault"
)

func TestPhotoOriginalsLivePairPrivateViewsAndIndependentRestore(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS native photo integration")
	}
	native, err := exec.LookPath("srics-image-preview")
	if err != nil {
		t.Fatal(err)
	}
	restic, err := exec.LookPath("restic")
	if err != nil {
		t.Fatal(err)
	}
	heic, err := os.ReadFile(filepath.Join("..", "..", "testdata", "media", "synthetic-photo.heic"))
	if err != nil {
		t.Fatal(err)
	}
	movie, err := os.ReadFile(filepath.Join("..", "..", "testdata", "media", "synthetic-photo.MOV"))
	if err != nil {
		t.Fatal(err)
	}
	var container bytes.Buffer
	z := zip.NewWriter(&container)
	for _, part := range []struct {
		name string
		data []byte
	}{{"synthetic-photo.heic", heic}, {"synthetic-photo.MOV", movie}} {
		w, _ := z.CreateHeader(&zip.FileHeader{Name: part.name, Method: zip.Store})
		w.Write(part.data)
	}
	z.Close()
	root := filepath.Join(t.TempDir(), "library")
	if err := library.Create(root); err != nil {
		t.Fatal(err)
	}
	l, err := library.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	wrapped, err := l.PrepareVault("synthetic-photo-vault", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := l.SetSetting("vault-key", wrapped); err != nil {
		t.Fatal(err)
	}
	key, err := vault.Unlock(wrapped, "synthetic-photo-vault")
	if err != nil {
		t.Fatal(err)
	}
	access := vault.NewAccess(context.Background(), key, time.Hour)
	defer access.Lock()
	c := media.Converter{CWebP: filepath.Join(t.TempDir(), "encoder-must-not-run"), NativePreview: native}
	photoIDs := []string{}
	for i, data := range [][]byte{heic, container.Bytes()} {
		name := "synthetic-photo.heic"
		if i == 1 {
			name = "synthetic-photo.livephoto.zip"
		}
		up, err := l.CreateUpload(library.Upload{ID: library.NewID(), Module: "photos", Name: name, Files: []library.UploadFile{{Name: name, Size: int64(len(data))}}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := l.Receive(context.Background(), up.ID, 0, fmt.Sprintf("%x", sha256.Sum256(data)), bytes.NewReader(data), c); err != nil {
			t.Fatal(err)
		}
		item, err := l.Finish(up.ID)
		if err != nil || item.Pages[0].Thumb == "" {
			t.Fatal("photo preview missing", err)
		}
		photoIDs = append(photoIDs, item.ID)
	}
	h := httptest.NewUnstartedServer(nil)
	s := New(context.Background(), h.Listener.Addr().String(), nil, nil)
	s.EnableLibrary(l, c, backup.Client{})
	s.library.sessions["photos"] = session{expires: time.Now().Add(time.Hour), csrf: "token", vault: access}
	h.Config.Handler = s
	h.Start()
	defer h.Close()
	call := func(method, path string, data []byte, headers map[string]string) (int, []byte, http.Header) {
		t.Helper()
		req, _ := http.NewRequest(method, h.URL+path, bytes.NewReader(data))
		req.AddCookie(&http.Cookie{Name: "srics_session", Value: "photos"})
		req.Header.Set("X-SRICS-Request", "app")
		req.Header.Set("X-SRICS-CSRF", "token")
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		res, err := h.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		return res.StatusCode, b, res.Header
	}
	for i, id := range photoIDs {
		code, b, _ := call("GET", "/api/items/"+id+"/download", nil, nil)
		want := heic
		if i == 1 {
			want = container.Bytes()
		}
		if code != 200 || !bytes.Equal(b, want) {
			t.Fatal("photo original changed", code)
		}
		for _, query := range []string{"", "?thumb=1"} {
			code, b, headers := call("GET", "/api/items/"+id+"/pages/0"+query, nil, nil)
			if _, _, err := image.Decode(bytes.NewReader(b)); code != 200 || err != nil || !media.SupportedImage(headers.Get("Content-Type")) {
				t.Fatal("photo preview", code, err)
			}
		}
	}
	code, b, _ := call("GET", "/api/items/"+photoIDs[1]+"/motion", nil, map[string]string{"Range": "bytes=0-23"})
	if code != 206 || !bytes.Equal(b, movie[:24]) {
		t.Fatal("motion range", code)
	}
	privateIDs := []string{}
	for i, data := range [][]byte{heic, container.Bytes()} {
		name := "private.heic"
		if i == 1 {
			name = "private.livephoto.zip"
		}
		id := library.NewID()
		code, b, _ := call("PUT", "/api/vault/items/"+id, data, map[string]string{"X-File-Name": url.PathEscape(name), "X-File-Module": "private", "X-File-Size": strconv.Itoa(len(data))})
		if code != 200 {
			t.Fatal("private upload", code, string(b))
		}
		var it library.PrivateItem
		json.Unmarshal(b, &it)
		if it.LivePhoto != (i == 1) {
			t.Fatal("live photo flag", it)
		}
		privateIDs = append(privateIDs, id)
		code, b, _ = call("GET", "/api/vault/items/"+id+"/download", nil, nil)
		if code != 200 || !bytes.Equal(b, data) {
			t.Fatal("private original changed", code)
		}
		for _, endpoint := range []string{"preview", "view"} {
			code, b, headers := call("GET", "/api/vault/items/"+id+"/"+endpoint, nil, nil)
			if _, _, err := image.Decode(bytes.NewReader(b)); code != 200 || err != nil || headers.Get("Cache-Control") != "no-store" {
				t.Fatal("private preview", code, err)
			}
		}
	}
	code, b, _ = call("GET", "/api/vault/items/"+privateIDs[1]+"/motion", nil, nil)
	if code != 200 || !bytes.Equal(b, movie) {
		t.Fatal("private motion changed", code)
	}
	access.Lock()
	code, _, _ = call("GET", "/api/vault/items/"+privateIDs[1]+"/motion", nil, nil)
	if code != 423 {
		t.Fatal("locked motion accessible", code)
	}
	stage := filepath.Join(root, "staging", "photo-test-snapshot")
	if err := l.Snapshot(context.Background(), stage); err != nil {
		t.Fatal(err)
	}
	client := backup.Client{Binary: restic, Repository: filepath.Join(t.TempDir(), "backup"), Password: "synthetic-photo-backup"}
	if err := client.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	snapshot, err := client.BackupLibrary(context.Background(), stage)
	if err != nil {
		t.Fatal(err)
	}
	h.Close()
	l.Close()
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	restoredPath := filepath.Join(t.TempDir(), "restored")
	if err := client.Restore(context.Background(), snapshot, restoredPath); err != nil {
		t.Fatal(err)
	}
	restored, err := library.Open(restoredPath)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if err := restored.Verify(context.Background()); err != nil {
		t.Fatal(err)
	}
	a := vault.NewAccess(context.Background(), key, time.Hour)
	defer a.Lock()
	for i, id := range photoIDs {
		it, err := restored.Item(id)
		if err != nil {
			t.Fatal(err)
		}
		f, err := restored.OpenOriginal(context.Background(), it.Pages[0])
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(f)
		f.Close()
		want := heic
		if i == 1 {
			want = container.Bytes()
		}
		if !bytes.Equal(data, want) {
			t.Fatal("restored photo changed")
		}
	}
	for i, id := range privateIDs {
		it, err := restored.PrivateItem(a, id)
		if err != nil {
			t.Fatal(err)
		}
		f, _, err := restored.PrivateRead(context.Background(), a, it, false)
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(f)
		f.Close()
		want := heic
		if i == 1 {
			want = container.Bytes()
		}
		if !bytes.Equal(data, want) || it.LivePhoto != (i == 1) {
			t.Fatal("restored private photo changed")
		}
	}
}
