//go:build integration

package library

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/soraincloud/srics-next/internal/media"
)

// This real 48 MP fixture belongs to the explicit integration suite: normal
// unit tests should not allocate hundreds of megabytes to check a boundary.
func TestLargeImageUploadConversionPreservationAndPreviews(t *testing.T) {
	palette := color.Palette{
		color.NRGBA{R: 9, G: 10, B: 11},
		color.NRGBA{R: 215, G: 67, B: 31, A: 255},
		color.NRGBA{R: 32, G: 173, B: 81, A: 128},
		color.NRGBA{R: 29, G: 53, B: 201, A: 255},
	}
	makePNG := func() []byte {
		im := image.NewPaletted(image.Rect(0, 0, 8000, 6000), palette)
		for i := range im.Pix {
			im.Pix[i] = byte(i/8000/3000*2 + i%8000/4000)
		}
		var b bytes.Buffer
		if err := png.Encode(&b, im); err != nil {
			t.Fatal(err)
		}
		return b.Bytes()
	}
	data := makePNG()
	l := testLibrary(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	up, err := l.CreateUpload(imageRequest(NewID(), "large-source.png", len(data)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = l.Receive(ctx, up.ID, 0, hash(data), bytes.NewReader(data), imageConverter(t)); err != nil {
		t.Fatal("48 MP PNG upload failed", err)
	}
	it, err := l.Finish(up.ID)
	if err != nil || it.Pages[0].MIME != "image/webp" || it.Pages[0].Thumb == "" {
		t.Fatal("converted original or preview missing", err)
	}
	stored, err := os.ReadFile(l.ObjectPath(it.Pages[0].Object))
	if err != nil {
		t.Fatal(err)
	}
	checkPixels := func() {
		decoded, kind, err := image.Decode(bytes.NewReader(stored))
		if err != nil || kind != "webp" || decoded.Bounds() != image.Rect(0, 0, 8000, 6000) {
			t.Fatal("full resolution not preserved", err)
		}
		for _, p := range []image.Point{{0, 0}, {7999, 0}, {0, 5999}, {7999, 5999}} {
			want := color.NRGBAModel.Convert(palette[p.Y/3000*2+p.X/4000])
			if color.NRGBAModel.Convert(decoded.At(p.X, p.Y)) != want {
				t.Fatal("large image color or alpha changed", p)
			}
		}
	}
	checkPixels()
	thumb, err := os.ReadFile(l.ObjectPath(it.Pages[0].Thumb))
	if err != nil {
		t.Fatal(err)
	}
	checkPreview := func(data []byte, format string) {
		cfg, kind, err := image.DecodeConfig(bytes.NewReader(data))
		if err != nil || kind != format || cfg.Width != 640 || cfg.Height != 480 {
			t.Fatal("large image preview missing or incorrectly sized", cfg, kind, err)
		}
	}
	checkPreview(thumb, "jpeg")
	// Reimport must never call an encoder, even above the old 40 MP limit.
	up, err = l.CreateUpload(imageRequest(NewID(), "large-existing.webp", len(stored)))
	if err != nil {
		t.Fatal(err)
	}
	missing := media.Converter{CWebP: filepath.Join(t.TempDir(), "encoder-must-not-run")}
	if _, err = l.Receive(ctx, up.ID, 0, hash(stored), bytes.NewReader(stored), missing); err != nil {
		t.Fatal("48 MP WebP reimport failed", err)
	}
	again, err := l.Finish(up.ID)
	if err != nil || again.Pages[0].SHA256 != hash(stored) {
		t.Fatal("large WebP reimport changed bytes", err)
	}
	original, err := os.ReadFile(l.ObjectPath(again.Pages[0].Object))
	if err != nil || !bytes.Equal(stored, original) {
		t.Fatal("large WebP original changed", err)
	}
	preview, err := privateThumbnail(data)
	if err != nil {
		t.Fatal("48 MP private photo preview rejected", err)
	}
	checkPreview(preview, "png")
	if err := l.Verify(ctx); err != nil {
		t.Fatal(err)
	}
}
