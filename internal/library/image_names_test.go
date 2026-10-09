package library

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/soraincloud/srics-next/internal/media"
)

func imageRequest(id, name string, size int) Upload {
	return Upload{ID: id, Module: "images", Name: name, Files: []UploadFile{{Name: name, Size: int64(size)}}}
}

func animatedImageFixture(t *testing.T) []byte {
	t.Helper()
	palette := color.Palette{color.Transparent, color.RGBA{R: 255, A: 255}, color.RGBA{B: 255, A: 255}}
	a, b := image.NewPaletted(image.Rect(0, 0, 8, 8), palette), image.NewPaletted(image.Rect(0, 0, 8, 8), palette)
	for i := range a.Pix {
		a.Pix[i], b.Pix[i] = 1, 2
	}
	var buf bytes.Buffer
	if err := gif.EncodeAll(&buf, &gif.GIF{Image: []*image.Paletted{a, b}, Delay: []int{10, 20}, LoopCount: 0}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestNumberedImagesConvertStaticAndPreserveGIFWebP(t *testing.T) {
	l := testLibrary(t)
	ctx := context.Background()
	png := fixture(t)
	im, _, _ := image.Decode(bytes.NewReader(png))
	var jpg bytes.Buffer
	if err := jpeg.Encode(&jpg, im, nil); err != nil {
		t.Fatal(err)
	}
	animation := animatedImageFixture(t)
	webp, err := os.ReadFile(filepath.Join("..", "..", "testdata", "media", "lossless-bare.webp"))
	if err != nil {
		t.Fatal(err)
	}
	formats := []struct {
		data []byte
		ext  string
	}{{png, ".webp"}, {jpg.Bytes(), ".webp"}, {animation, ".gif"}, {webp, ".webp"}}
	for i, format := range formats {
		source := fmt.Sprintf("来源文件名-杂乱-%d.错误扩展名", i)
		req := imageRequest(NewID(), source, len(format.data))
		up, err := l.CreateUpload(req)
		want := fmt.Sprintf("IMG-%06d", i+1)
		if err != nil || !up.AutoName || up.Name != want || up.Files[0].Name != want {
			t.Fatal("generated task", up, err)
		}
		c := media.Converter{CWebP: filepath.Join(t.TempDir(), "must-not-run")}
		if i < 2 {
			c = imageConverter(t)
		}
		if _, err = l.Receive(ctx, up.ID, 0, hash(format.data), bytes.NewReader(format.data), c); err != nil {
			t.Fatal(err)
		}
		it, err := l.Finish(up.ID)
		if err != nil || it.Name != want || it.Pages[0].Name != want+format.ext {
			t.Fatal("normalized item", it, err)
		}
		original, err := os.ReadFile(l.ObjectPath(it.Pages[0].Object))
		if err != nil || it.Pages[0].SHA256 != hash(original) {
			t.Fatal("stored image failed integrity check", err)
		}
		if i < 2 {
			source, _, _ := image.Decode(bytes.NewReader(format.data))
			stored, kind, err := image.Decode(bytes.NewReader(original))
			if err != nil || kind != "webp" || !media.EqualPixels(source, stored) {
				t.Fatal("static image conversion changed pixels", err)
			}
		} else if !bytes.Equal(original, format.data) || (i == 2 && it.Pages[0].Thumb != "") {
			t.Fatal("WebP/GIF was re-encoded or GIF animation hidden")
		}
		// Lost registration and completion responses retain exactly one number.
		if repeated, e := l.CreateUpload(req); e != nil || repeated.Name != want || repeated.State != "complete" {
			t.Fatal("registration retry", repeated, e)
		}
		if repeated, e := l.Finish(up.ID); e != nil || repeated.ID != it.ID || repeated.Pages[0].Name != it.Pages[0].Name {
			t.Fatal("completion retry", repeated, e)
		}
		saved, err := l.Upload(up.ID)
		encoded, _ := json.Marshal(saved)
		if err != nil || strings.Contains(string(encoded), source) {
			t.Fatal("source name was retained", string(encoded), err)
		}
	}
	if err := l.Verify(ctx); err != nil {
		t.Fatal(err)
	}
	// A copied backup index retains both file names and the sequence progress.
	snapshot := filepath.Join(t.TempDir(), "snapshot")
	if err := l.Snapshot(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	restored, err := Open(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if err := restored.Verify(ctx); err != nil {
		t.Fatal(err)
	}
	items, err := restored.Items("images", false)
	if err != nil || len(items) != 4 || items[0].Pages[0].Name != "IMG-000004.webp" {
		t.Fatal("snapshot lost normalized names", items, err)
	}
	next, err := restored.CreateUpload(imageRequest(NewID(), "another.png", len(png)))
	if err != nil || next.Name != "IMG-000005" {
		t.Fatal("restored sequence was reused", next, err)
	}
}

func TestImageConversionFailureCanRetryAndCompletedOriginalsStayUnchanged(t *testing.T) {
	l := testLibrary(t)
	data := fixture(t)
	up, err := l.CreateUpload(imageRequest(NewID(), "source.png", len(data)))
	if err != nil {
		t.Fatal(err)
	}
	missing := media.Converter{CWebP: filepath.Join(t.TempDir(), "missing-encoder")}
	if _, err = l.Receive(context.Background(), up.ID, 0, hash(data), bytes.NewReader(data), missing); err == nil {
		t.Fatal("conversion failure was accepted")
	}
	if _, err = l.Finish(up.ID); err == nil {
		t.Fatal("failed conversion published an item")
	}
	if _, err = l.Receive(context.Background(), up.ID, 0, hash(data), bytes.NewReader(data), imageConverter(t)); err != nil {
		t.Fatal("failed conversion could not retry", err)
	}
	if _, err = l.Receive(context.Background(), up.ID, 0, hash(data), bytes.NewReader(data), missing); err != nil {
		t.Fatal("completed page was re-encoded on retry", err)
	}
	it, err := l.Finish(up.ID)
	if err != nil || it.Pages[0].Name != "IMG-000001.webp" {
		t.Fatal("retry changed numbering", err)
	}
	// A page already received by an old version remains its original PNG.
	p, err := l.put(data)
	if err != nil {
		t.Fatal(err)
	}
	p.Name, p.MIME = "old.png", "image/png"
	legacy := imageRequest(NewID(), "old.png", len(data))
	legacy.State, legacy.Files[0].Page, legacy.Files[0].SourceHash = "pending", &p, hash(data)
	if err = l.saveUpload(legacy); err != nil {
		t.Fatal(err)
	}
	if _, err = l.Receive(context.Background(), legacy.ID, 0, hash(data), bytes.NewReader(data), missing); err != nil {
		t.Fatal("legacy completed page was re-encoded", err)
	}
	it, err = l.Finish(legacy.ID)
	if err != nil || it.Pages[0].Name != "old.png" || it.Pages[0].SHA256 != hash(data) {
		t.Fatal("legacy original changed", err)
	}
}

func TestImageNumberReservationIsAtomicAndNeverReused(t *testing.T) {
	l := testLibrary(t)
	req := imageRequest(NewID(), "source.png", 100)
	if _, err := l.db.Exec(`CREATE TRIGGER fail_image_registration BEFORE INSERT ON uploads BEGIN SELECT RAISE(FAIL,'synthetic registration failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := l.CreateUpload(req); err == nil {
		t.Fatal("failed registration succeeded")
	}
	if number, err := l.Setting(imageSequenceSetting); err != nil || len(number) != 0 {
		t.Fatal("failed task committed a number", string(number), err)
	}
	if _, err := l.db.Exec("DROP TRIGGER fail_image_registration"); err != nil {
		t.Fatal(err)
	}
	first, err := l.CreateUpload(req)
	if err != nil || first.Name != "IMG-000001" {
		t.Fatal(first, err)
	}
	changed := imageRequest(req.ID, "renamed.png", 101)
	if _, err = l.CreateUpload(changed); !errors.Is(err, ErrConflict) {
		t.Fatal("changed task was accepted", err)
	}
	if err = l.Cancel(first.ID); err != nil {
		t.Fatal(err)
	}
	if err = l.Collect(); err != nil {
		t.Fatal(err)
	}
	root := l.Root
	l.Close()
	l, err = Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	next, err := l.CreateUpload(imageRequest(NewID(), "new.png", 100))
	if err != nil || next.Name != "IMG-000002" {
		t.Fatal("cancelled number reused after restart", next, err)
	}
	if err = l.SetSetting(imageSequenceSetting, []byte("damaged")); err != nil {
		t.Fatal(err)
	}
	if _, err = l.CreateUpload(imageRequest(NewID(), "new.png", 100)); err == nil {
		t.Fatal("damaged sequence silently reset")
	}
}

func TestNumberedImageChunkResumeChecksBytesAfterSourceRename(t *testing.T) {
	l := testLibrary(t)
	ctx := context.Background()
	data := fixture(t)
	source := "原始图片名-with-来源信息.webp"
	up, err := l.CreateUpload(imageRequest(NewID(), source, len(data)))
	if err != nil {
		t.Fatal(err)
	}
	tr := Transfer{ID: NewID(), Parent: up.ID, Module: "images", Name: source, Size: int64(len(data)), Hashes: []string{hash(data)}}
	saved, err := l.CreateTransfer(ctx, tr, nil)
	if err != nil || saved.Name != up.Name {
		t.Fatal("transfer retained source name", saved, err)
	}
	if _, err = l.ReceiveChunk(ctx, tr.ID, 0, bytes.NewReader(data), nil); err != nil {
		t.Fatal(err)
	}
	root := l.Root
	l.Close()
	l, err = Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	tr.Name = "下载后重新命名.png"
	resumed, err := l.CreateTransfer(ctx, tr, nil)
	if err != nil || len(resumed.Done) != 1 || resumed.Name != up.Name {
		t.Fatal("renamed source could not resume", resumed, err)
	}
	wrong := tr
	wrong.Hashes = []string{hash(bytes.Repeat([]byte{1}, len(data)))}
	if _, err = l.CreateTransfer(ctx, wrong, nil); err == nil {
		t.Fatal("same-size different image mixed into saved chunks")
	}
	if _, err = l.FinishTransfer(ctx, tr.ID, nil, imageConverter(t)); err != nil {
		t.Fatal(err)
	}
	it, err := l.Finish(up.ID)
	if err != nil || it.Pages[0].Name != "IMG-000001.webp" {
		t.Fatal("incorrect actual-format extension", it, err)
	}
	if _, err = l.CreateTransfer(ctx, tr, nil); err != nil {
		t.Fatal("completed transfer retry", err)
	}
	var payload []byte
	if err = l.db.QueryRow("SELECT payload FROM transfers WHERE id=?", tr.ID).Scan(&payload); err != nil || strings.Contains(string(payload), source) || strings.Contains(string(payload), tr.Name) {
		t.Fatal("transfer persisted a source filename", string(payload), err)
	}
}

func TestLegacyImageTasksKeepIdentityAndPersonalPhotosKeepNames(t *testing.T) {
	l := testLibrary(t)
	data := fixture(t)
	legacy := imageRequest(NewID(), "IMG-000009.png", len(data))
	legacy.Tags = []string{}
	legacy.State = "pending"
	if err := l.saveUpload(legacy); err != nil {
		t.Fatal(err)
	}
	repeated, err := l.CreateUpload(legacy)
	if err != nil || repeated.AutoName || repeated.Name != legacy.Name {
		t.Fatal("old task identity changed", repeated, err)
	}
	if _, err = l.Receive(context.Background(), legacy.ID, 0, hash(data), bytes.NewReader(data), imageConverter(t)); err != nil {
		t.Fatal(err)
	}
	it, err := l.Finish(legacy.ID)
	if err != nil || it.Name != legacy.Name || it.Pages[0].Name != "IMG-000009.webp" {
		t.Fatal("legacy task identity changed or extension mismatched", it, err)
	}
	newTask, err := l.CreateUpload(imageRequest(NewID(), "random.png", len(data)))
	if err != nil || newTask.Name != "IMG-000010" {
		t.Fatal("existing normalized filename collided", newTask, err)
	}
	photo := Upload{ID: NewID(), Module: "photos", Name: "旅行照片", AutoName: true, Files: []UploadFile{{Name: "DSC_1123.png", Size: int64(len(data))}}}
	photoTask, err := l.CreateUpload(photo)
	if err != nil || photoTask.AutoName || photoTask.Name != photo.Name || photoTask.Files[0].Name != photo.Files[0].Name {
		t.Fatal("personal photo names changed", photoTask, err)
	}
}
