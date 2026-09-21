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
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"

	"filippo.io/age"
	"github.com/soraincloud/srics-next/internal/backup"
	"github.com/soraincloud/srics-next/internal/library"
	"github.com/soraincloud/srics-next/internal/media"
	"github.com/soraincloud/srics-next/internal/vault"
)

// Exercise the real HTTP chunk-upload, publication, original-view and download
// routes. Each generation imports the preceding download, not the fixture again.
func TestWebPHTTPRepeatedImportDownloadAndClassification(t *testing.T) {
	root := filepath.Join(t.TempDir(), "library")
	if err := library.Create(root); err != nil {
		t.Fatal(err)
	}
	l, err := library.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := httptest.NewUnstartedServer(nil)
	s := New(ctx, h.Listener.Addr().String(), fstest.MapFS{"index.html": {Data: []byte("app")}}, nil)
	// An accidental re-encode must fail, even when cwebp exists on the test host.
	s.EnableLibrary(l, media.Converter{CWebP: filepath.Join(t.TempDir(), "missing-encoder")}, backup.Client{})
	key, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	a := vault.NewAccess(ctx, key, time.Hour)
	defer a.Lock()
	s.library.sessions["preservation"] = session{expires: time.Now().Add(time.Hour), csrf: "token", vault: a}
	h.Config.Handler = s
	h.Start()
	defer h.Close()
	request := func(t *testing.T, method, path string, body []byte) []byte {
		t.Helper()
		r, err := http.NewRequest(method, h.URL+path, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("X-SRICS-Request", "app")
		r.Header.Set("X-SRICS-CSRF", "token")
		r.AddCookie(&http.Cookie{Name: "srics_session", Value: "preservation"})
		res, err := h.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		data, err := io.ReadAll(res.Body)
		if err != nil || res.StatusCode != 200 {
			t.Fatalf("%s %s: %d %s: %v", method, path, res.StatusCode, data, err)
		}
		return data
	}
	originals := make([][]byte, 2)
	for i, name := range []string{"lossless-alpha.webp", "lossy-alpha.webp"} {
		originals[i], err = os.ReadFile(filepath.Join("..", "..", "testdata", "media", name))
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, module := range []string{"comics", "images", "photos", "private", "files"} {
		t.Run(module, func(t *testing.T) {
			call := func(method, path string, body []byte) []byte {
				t.Helper()
				return request(t, method, path, body)
			}
			post := func(path string, value any) []byte {
				t.Helper()
				body, err := json.Marshal(value)
				if err != nil {
					t.Fatal(err)
				}
				return call("POST", path, body)
			}

			current := [][]byte{bytes.Clone(originals[0]), bytes.Clone(originals[1])}
			for round := 0; round < 10; round++ {
				// Comics deliberately arrive out of numerical order in one book.
				var book library.Upload
				if module == "comics" {
					book = library.Upload{ID: library.NewID(), Module: module, Name: "同名漫画", Files: []library.UploadFile{
						{Name: "10.png", Size: int64(len(current[1]))}, {Name: "2.png", Size: int64(len(current[0]))},
					}}
					post("/api/uploads", book)
				}
				for i, data := range current {
					private := module == "private" || module == "files"
					prefix := "/api"
					if private {
						prefix += "/vault"
					}
					// Wrong extension proves the content, not the filename, decides conversion.
					name := "same-name.png"
					parent, index := "", 0
					if module == "comics" {
						parent, index = book.ID, 1-i
						name = book.Files[index].Name
					} else if !private {
						parent = library.NewID()
						post("/api/uploads", library.Upload{ID: parent, Module: module, Name: name, Files: []library.UploadFile{{Name: name, Size: int64(len(data))}}})
					}
					tr := library.Transfer{ID: library.NewID(), Parent: parent, Index: index, Module: module, Name: name, Size: int64(len(data)), Hashes: []string{fmt.Sprintf("%x", sha256.Sum256(data))}}
					post(prefix+"/transfers", tr)
					call("PUT", prefix+"/transfers/"+tr.ID+"/chunks/0", data)
					call("POST", prefix+"/transfers/"+tr.ID+"/finish", nil)
					// Retrying a lost completion response must not transform the original.
					call("POST", prefix+"/transfers/"+tr.ID+"/finish", nil)
					if module == "comics" {
						continue
					}
					id := tr.ID
					if !private {
						id = parent
						call("POST", "/api/uploads/"+parent+"/finish", nil)
						view := call("GET", "/api/items/"+id+"/pages/0", nil)
						if !bytes.Equal(view, originals[i]) {
							t.Fatal("full-size view used transformed bytes")
						}
					}
					current[i] = call("GET", prefix+"/items/"+id+"/download", nil)
				}
				if module == "comics" {
					call("POST", "/api/uploads/"+book.ID+"/finish", nil)
					data := call("GET", "/api/items/"+book.ID+"/download", nil)
					z, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
					if err != nil {
						t.Fatal(err)
					}
					if len(z.File) != 2 {
						t.Fatal("missing comic pages")
					}
					for i, f := range z.File {
						if f.Name != fmt.Sprintf("同名漫画/%05d.webp", i+1) {
							t.Fatal("wrong export hierarchy/order", f.Name)
						}
						r, err := f.Open()
						if err != nil {
							t.Fatal(err)
						}
						current[i], err = io.ReadAll(r)
						r.Close()
						if err != nil {
							t.Fatal(err)
						}
						view := call("GET", fmt.Sprintf("/api/items/%s/pages/%d", book.ID, i), nil)
						if !bytes.Equal(view, originals[i]) {
							t.Fatal("comic reader used transformed bytes")
						}
					}
				}
				for i := range current {
					if !bytes.Equal(current[i], originals[i]) || sha256.Sum256(current[i]) != sha256.Sum256(originals[i]) {
						t.Fatalf("generation %d page %d: original changed", round, i)
					}
				}
			}
		})
	}
	for _, module := range []string{"comics", "images", "photos"} {
		items, err := l.Items(module, false)
		want := 20
		if module == "comics" {
			want = 10
		}
		if err != nil || len(items) != want {
			t.Fatalf("%s classification/count: %d, %v", module, len(items), err)
		}
		for _, it := range items {
			if it.Module != module {
				t.Fatal("module mixed with another collection")
			}
			for _, p := range it.Pages {
				if filepath.Dir(l.ObjectPath(p.Object)) != filepath.Join(root, "objects") {
					t.Fatal("unexpected original location")
				}
				b, err := os.ReadFile(l.ObjectPath(p.Object))
				if err != nil || fmt.Sprintf("%x", sha256.Sum256(b)) != p.SHA256 {
					t.Fatal("stored original hash mismatch", err)
				}
			}
		}
	}
	privateItems, err := l.PrivateItems(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, it := range privateItems {
		counts[it.Module]++
		cipher, err := os.ReadFile(l.PrivatePath(it.Object))
		if err != nil || !bytes.HasPrefix(cipher, []byte("age-encryption.org/v1")) {
			t.Fatal("private original not encrypted", err)
		}
		if _, err := os.Stat(l.ObjectPath(it.Object)); !os.IsNotExist(err) {
			t.Fatal("private original leaked into ordinary objects")
		}
	}
	if counts["private"] != 20 || counts["files"] != 20 || len(counts) != 2 {
		t.Fatal("private classification", counts)
	}
	if err := l.Collect(); err != nil {
		t.Fatal(err)
	}
	if err := l.Verify(ctx); err != nil {
		t.Fatal(err)
	}
}
