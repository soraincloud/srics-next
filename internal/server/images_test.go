package server

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/gif"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/soraincloud/srics-next/internal/backup"
	"github.com/soraincloud/srics-next/internal/library"
	"github.com/soraincloud/srics-next/internal/media"
)

func TestMixedWebPGIFChunkUploadPreviewDownloadAndRandom(t *testing.T) {
	l := integrityLibrary(t)
	palette := color.Palette{color.Black, color.White}
	a, b := image.NewPaletted(image.Rect(0, 0, 8, 8), palette), image.NewPaletted(image.Rect(0, 0, 8, 8), palette)
	for i := range b.Pix {
		b.Pix[i] = 1
	}
	var animated bytes.Buffer
	if err := gif.EncodeAll(&animated, &gif.GIF{Image: []*image.Paletted{a, b}, Delay: []int{10, 20}, LoopCount: 0}); err != nil {
		t.Fatal(err)
	}
	webp, err := os.ReadFile(filepath.Join("..", "..", "testdata", "media", "lossy-alpha.webp"))
	if err != nil {
		t.Fatal(err)
	}
	h := httptest.NewUnstartedServer(nil)
	s := New(context.Background(), h.Listener.Addr().String(), nil, nil)
	s.EnableLibrary(l, media.Converter{CWebP: filepath.Join(t.TempDir(), "must-not-run")}, backup.Client{})
	s.library.sessions["images"] = session{expires: time.Now().Add(time.Hour), csrf: "token"}
	h.Config.Handler = s
	h.Start()
	defer h.Close()
	call := func(method, path string, body []byte) ([]byte, http.Header) {
		t.Helper()
		req, _ := http.NewRequest(method, h.URL+path, bytes.NewReader(body))
		req.Header.Set("X-SRICS-Request", "app")
		req.Header.Set("X-SRICS-CSRF", "token")
		req.AddCookie(&http.Cookie{Name: "srics_session", Value: "images"})
		res, err := h.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		data, err := io.ReadAll(res.Body)
		if err != nil || res.StatusCode != 200 {
			t.Fatalf("%s %s: %d %s %v", method, path, res.StatusCode, data, err)
		}
		return data, res.Header
	}
	post := func(path string, value any) []byte {
		payload, _ := json.Marshal(value)
		data, _ := call("POST", path, payload)
		return data
	}
	items := []library.Item{}
	for i, data := range [][]byte{animated.Bytes(), webp} {
		id := library.NewID()
		post("/api/uploads", library.Upload{ID: id, Module: "images", Name: "wrong-source.png", Files: []library.UploadFile{{Name: "wrong-source.png", Size: int64(len(data))}}})
		tr := library.Transfer{ID: library.NewID(), Parent: id, Module: "images", Name: "wrong-source.png", Size: int64(len(data)), Hashes: []string{fmt.Sprintf("%x", sha256.Sum256(data))}}
		post("/api/transfers", tr)
		call("PUT", "/api/transfers/"+tr.ID+"/chunks/0", data)
		call("POST", "/api/transfers/"+tr.ID+"/finish", nil)
		call("POST", "/api/transfers/"+tr.ID+"/finish", nil)
		body, _ := call("POST", "/api/uploads/"+id+"/finish", nil)
		var item library.Item
		if err = json.Unmarshal(body, &item); err != nil {
			t.Fatal(err)
		}
		ext := ".gif"
		if i == 1 {
			ext = ".webp"
		}
		want := fmt.Sprintf("IMG-%06d%s", i+1, ext)
		original, headers := call("GET", "/api/items/"+id+"/download", nil)
		_, name, err := mime.ParseMediaType(headers.Get("Content-Disposition"))
		if err != nil || name["filename"] != want || item.Pages[0].Name != want || !bytes.Equal(original, data) {
			t.Fatal("mixed image name or original changed", want, name, err)
		}
		items = append(items, item)
	}
	// Emulate a pre-update GIF with an old static JPEG thumbnail.
	db, err := sql.Open("srics-sqlite3", filepath.Join(l.Root, "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(`UPDATE items SET pages=json_set(pages,'$[0].thumb',?) WHERE id=?`, items[1].Pages[0].Thumb, items[0].ID); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"", "?thumb=1"} {
		data, headers := call("GET", "/api/items/"+items[0].ID+"/pages/0"+query, nil)
		if headers.Get("Content-Type") != "image/gif" || !bytes.Equal(data, animated.Bytes()) {
			t.Fatal("GIF preview lost animation", query, headers)
		}
	}
	for _, query := range []string{"", "&random=1"} {
		body, _ := call("GET", "/api/library?module=images"+query, nil)
		var listing struct {
			Items []library.Item `json:"items"`
		}
		if err := json.Unmarshal(body, &listing); err != nil || len(listing.Items) != 2 {
			t.Fatal("mixed list/random pool incomplete", err)
		}
	}
	data, _ := call("GET", "/api/download?id="+items[0].ID+"&id="+items[1].ID, nil)
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil || len(archive.File) != 2 {
		t.Fatal("mixed batch download", err)
	}
	for i, expected := range [][]byte{animated.Bytes(), webp} {
		file, err := archive.File[i].Open()
		if err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(file)
		file.Close()
		if err != nil || !bytes.Equal(got, expected) || archive.File[i].Name != items[i].Pages[0].Name {
			t.Fatal("mixed archive changed GIF/WebP", err)
		}
	}
}
