//go:build integration

package media

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestNativePhotoFormatsConversionAndOriginalPreview(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS ImageIO integration")
	}
	native, err := exec.LookPath("srics-image-preview")
	if err != nil {
		t.Fatal("build native preview helper first", err)
	}
	encoder, err := exec.LookPath("cwebp")
	if err != nil {
		t.Fatal(err)
	}
	c := Converter{CWebP: encoder, NativePreview: native}
	for _, ext := range []string{"heic", "avif", "bmp", "tiff"} {
		t.Run(ext, func(t *testing.T) {
			input, err := os.ReadFile(filepath.Join("..", "..", "testdata", "media", "synthetic-photo."+ext))
			if err != nil {
				t.Fatal(err)
			}
			original := bytes.Clone(input)
			preview, err := c.Preview(context.Background(), input, 16)
			if err != nil {
				t.Fatal("preview", err)
			}
			cfg, err := png.DecodeConfig(bytes.NewReader(preview.Data))
			if err != nil || cfg.Width > 16 || cfg.Height > 16 {
				t.Fatal("preview size", cfg, err)
			}
			full, err := c.Preview(context.Background(), input, 0)
			if err != nil {
				t.Fatal("canonical decode", err)
			}
			result, kind, err := c.ImportImage(context.Background(), input)
			if err != nil || result.Preserved || kind != "image/webp" {
				t.Fatal("static collection conversion", kind, err)
			}
			want, _, _ := image.Decode(bytes.NewReader(full.Data))
			got, _, err := image.Decode(bytes.NewReader(result.Data))
			if err != nil || !EqualPixels(want, got) || !bytes.Equal(input, original) {
				t.Fatal("conversion changed decoded pixels or input", err)
			}
			repeated, err := (Converter{}).Convert(context.Background(), result.Data)
			if err != nil || !repeated.Preserved || !bytes.Equal(result.Data, repeated.Data) {
				t.Fatal("reimport changed WebP", err)
			}
		})
	}
	var high bytes.Buffer
	if err := png.Encode(&high, image.NewNRGBA64(image.Rect(0, 0, 8, 6))); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Preview(context.Background(), high.Bytes(), 0); err == nil || !strings.Contains(err.Error(), "高位深") {
		t.Fatal("16-bit native conversion was not rejected", err)
	}
	if _, err := c.Preview(context.Background(), high.Bytes(), 16); err != nil {
		t.Fatal("16-bit original preview unavailable", err)
	}
}
