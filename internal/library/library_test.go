package library

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	"github.com/soraincloud/srics-next/internal/media"
)

func fixture(t *testing.T) []byte {
	t.Helper()
	im := image.NewNRGBA(image.Rect(0, 0, 8, 8))
	im.SetNRGBA(1, 1, color.NRGBA{R: 220, G: 30, B: 60, A: 255})
	var b bytes.Buffer
	if err := png.Encode(&b, im); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
func testLibrary(t *testing.T) *Library {
	t.Helper()
	root := filepath.Join(t.TempDir(), "library")
	if err := Create(root); err != nil {
		t.Fatal(err)
	}
	l, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	return l
}
func hash(data []byte) string { h := sha256.Sum256(data); return hex.EncodeToString(h[:]) }

func imageConverter(t *testing.T) media.Converter {
	t.Helper()
	binary, err := exec.LookPath("cwebp")
	if err != nil {
		t.Skip("requires cwebp for static image conversion")
	}
	return media.Converter{CWebP: binary}
}

func TestWebPThumbnailNeverReplacesOriginal(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "media", "lossless-bare.webp"))
	if err != nil {
		t.Fatal(err)
	}
	l := testLibrary(t)
	for _, module := range []string{"comics", "images", "photos"} {
		it := upload(t, l, module, "original.webp", data)
		p := it.Pages[0]
		if p.Thumb == "" || p.Thumb == p.Object {
			t.Fatalf("%s thumbnail must have its own object", module)
		}
		thumb, err := os.ReadFile(l.ObjectPath(p.Thumb))
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Equal(thumb, data) {
			t.Fatal("test must exercise an actually re-encoded preview")
		}
		if _, format, err := image.Decode(bytes.NewReader(thumb)); err != nil || format != "jpeg" {
			t.Fatal("invalid thumbnail", err)
		}
		original, err := os.ReadFile(l.ObjectPath(p.Object))
		if err != nil || !bytes.Equal(original, data) || p.SHA256 != hash(data) {
			t.Fatalf("%s thumbnail generation altered original: %v", module, err)
		}
	}
}

func upload(t *testing.T, l *Library, module, name string, data []byte) Item {
	t.Helper()
	up, err := l.CreateUpload(Upload{ID: NewID(), Module: module, Name: name, Files: []UploadFile{{Name: name, Size: int64(len(data))}}})
	if err != nil {
		t.Fatal(err)
	}
	c := media.Converter{}
	if module == "images" {
		c = imageConverter(t)
	}
	if _, err = l.Receive(context.Background(), up.ID, 0, hash(data), bytes.NewReader(data), c); err != nil {
		t.Fatal(err)
	}
	it, err := l.Finish(up.ID)
	if err != nil {
		t.Fatal(err)
	}
	return it
}
func TestRetryRestartOriginalsAndTrash(t *testing.T) {
	l := testLibrary(t)
	data := fixture(t)
	up, err := l.CreateUpload(Upload{ID: NewID(), Module: "photos", Name: "same.png", Files: []UploadFile{{Name: "same.png", Size: int64(len(data))}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = l.Receive(context.Background(), up.ID, 0, hash(data), bytes.NewReader(data[:len(data)/2]), media.Converter{}); err == nil {
		t.Fatal("truncated upload accepted")
	}
	if _, err = l.Finish(up.ID); err == nil {
		t.Fatal("incomplete upload committed")
	}
	items, _ := l.Items("photos", false)
	if len(items) != 0 {
		t.Fatal("partial item visible")
	}
	l.Close()
	l, err = Open(l.Root)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	saved, err := l.Receive(context.Background(), up.ID, 0, hash(data), bytes.NewReader(data), media.Converter{})
	if err != nil {
		t.Fatal(err)
	}
	retry, err := l.Receive(context.Background(), up.ID, 0, hash(data), bytes.NewReader(data), media.Converter{})
	if err != nil || saved.Files[0].Page.Object != retry.Files[0].Page.Object {
		t.Fatal("retry duplicated object", err)
	}
	wrong := append([]byte{}, data...)
	wrong[0] ^= 1
	if _, err = l.Receive(context.Background(), up.ID, 0, hash(wrong), bytes.NewReader(wrong), media.Converter{}); err == nil {
		t.Fatal("changed retry accepted")
	}
	it, err := l.Finish(up.ID)
	if err != nil {
		t.Fatal(err)
	}
	again, err := l.Finish(up.ID)
	if err != nil || it.ID != again.ID {
		t.Fatal("finish duplicated item", err)
	}
	same := upload(t, l, "photos", "same.png", data)
	if it.ID == same.ID {
		t.Fatal("same name overwritten")
	}
	raw, err := os.ReadFile(l.ObjectPath(it.Pages[0].Object))
	if err != nil || !bytes.Equal(raw, data) {
		t.Fatal("source bytes changed", err)
	}
	if err = l.Trash(it.ID, false); err != nil {
		t.Fatal(err)
	}
	items, _ = l.Items("photos", false)
	trash, _ := l.Items("all", true)
	if len(items) != 1 || len(trash) != 1 {
		t.Fatal("trash scope incorrect")
	}
	if err = l.Trash(it.ID, true); err != nil {
		t.Fatal(err)
	}
	items, _ = l.Items("photos", false)
	if len(items) != 2 {
		t.Fatal("restore failed")
	}
}
func TestMissingDiskSpaceAndOneProcess(t *testing.T) {
	l := testLibrary(t)
	if other, err := Open(l.Root); err == nil {
		other.Close()
		t.Fatal("second server acquired data")
	}
	l.FreeSpace = func(string) (uint64, error) { return 0, nil }
	data := fixture(t)
	up, err := l.CreateUpload(Upload{ID: NewID(), Module: "images", Name: "x.png", Files: []UploadFile{{Name: "x.png", Size: int64(len(data))}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = l.Receive(context.Background(), up.ID, 0, hash(data), bytes.NewReader(data), media.Converter{}); err == nil {
		t.Fatal("low disk accepted")
	}
	items, _ := l.Items("images", false)
	if len(items) != 0 {
		t.Fatal("low disk marked success")
	}
	if err = os.Rename(filepath.Join(l.Root, "format"), filepath.Join(l.Root, "removed-format")); err != nil {
		t.Fatal(err)
	}
	if err = l.Check(); err == nil {
		t.Fatal("missing disk not detected")
	}
	if _, err = l.Items("all", false); err == nil {
		t.Fatal("stale index served")
	}
}
func TestComicWholeImportOrderAndConflict(t *testing.T) {
	cwebp, err := exec.LookPath("cwebp")
	if err != nil {
		t.Skip("cwebp required for conversion integration")
	}
	l := testLibrary(t)
	data := fixture(t)
	up, err := l.CreateUpload(Upload{ID: NewID(), Module: "comics", Name: "一本漫画", Tags: []string{"冒险", "冒险", "日常"}, Files: []UploadFile{{Name: "10.png", Size: int64(len(data))}, {Name: "2.png", Size: int64(len(data))}, {Name: "1.png", Size: int64(len(data))}}})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err = l.Receive(context.Background(), up.ID, i, hash(data), bytes.NewReader(data), media.Converter{CWebP: cwebp}); err != nil {
			t.Fatal(err)
		}
		if i < 2 {
			if _, err = l.Finish(up.ID); err == nil {
				t.Fatal("incomplete comic committed")
			}
		}
	}
	it, err := l.Finish(up.ID)
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{"1.png", "2.png", "10.png"} {
		if it.Pages[i].Name != want {
			t.Fatal("incorrect natural ordering", it.Pages)
		}
	}
	if len(it.Tags) != 2 {
		t.Fatal("duplicate tag")
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); results <- l.Update(it.ID, "新名称", []string{"测试"}, it.Revision) }()
	}
	wg.Wait()
	close(results)
	success := 0
	for err = range results {
		if err == nil {
			success++
		}
	}
	if success != 1 {
		t.Fatal("stale update overwrote newer revision")
	}
	bad := []byte("not an image")
	up, err = l.CreateUpload(Upload{ID: NewID(), Module: "comics", Name: "坏图", Files: []UploadFile{{Name: "bad.webp", Size: int64(len(bad))}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = l.Receive(context.Background(), up.ID, 0, hash(bad), bytes.NewReader(bad), media.Converter{CWebP: cwebp}); err == nil {
		t.Fatal("bad image accepted")
	}
}
func TestSnapshotPinsIndexAndResumableFiles(t *testing.T) {
	l := testLibrary(t)
	data := fixture(t)
	first := upload(t, l, "images", "first.png", data)
	_ = l.Trash(first.ID, false)
	up, _ := l.CreateUpload(Upload{ID: NewID(), Module: "photos", Name: "pending", Files: []UploadFile{{Name: "a.png", Size: int64(len(data))}}})
	if _, err := l.Receive(context.Background(), up.ID, 0, hash(data), bytes.NewReader(data), media.Converter{}); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(filepath.Dir(l.Root), "snapshot")
	if err := l.Snapshot(context.Background(), dest); err != nil {
		t.Fatal(err)
	}
	upload(t, l, "images", "later.png", data)
	restored, err := Open(dest)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	items, _ := restored.Items("all", false)
	trash, _ := restored.Items("all", true)
	if len(items) != 0 || len(trash) != 1 {
		t.Fatal("snapshot is inconsistent")
	}
	if _, err = restored.Finish(up.ID); err != nil {
		t.Fatal("pending upload not resumable after restore", err)
	}
	if err = restored.Verify(context.Background()); err != nil {
		t.Fatal(err)
	}
}
func TestManifestRejectsUnexpectedModulesAndPaths(t *testing.T) {
	l := testLibrary(t)
	for _, module := range []string{"private", "files", "novels", "../../../"} {
		if _, err := l.CreateUpload(Upload{ID: NewID(), Module: module, Name: "name", Files: []UploadFile{{Name: "a.png", Size: 1}}}); err == nil {
			t.Fatal("unavailable module accepted", module)
		}
	}
	for _, name := range []string{"../a.png", "nested/a.png", "bad\\a.png", ".DS_Store", ""} {
		if _, err := l.CreateUpload(Upload{ID: NewID(), Module: "comics", Name: "name", Files: []UploadFile{{Name: name, Size: 1}}}); err == nil {
			t.Fatal("invalid manifest name", name)
		}
	}
}
