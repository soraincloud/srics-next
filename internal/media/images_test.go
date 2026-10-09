package media

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/gif"
	"os"
	"path/filepath"
	"testing"
)

func animationFixture(t *testing.T) []byte {
	t.Helper()
	palette := color.Palette{color.NRGBA{A: 0}, color.NRGBA{R: 255, A: 255}, color.NRGBA{B: 255, A: 255}}
	a, b := image.NewPaletted(image.Rect(0, 0, 8, 8), palette), image.NewPaletted(image.Rect(0, 0, 8, 8), palette)
	for i := range a.Pix {
		a.Pix[i], b.Pix[i] = 1, 2
	}
	var out bytes.Buffer
	if err := gif.EncodeAll(&out, &gif.GIF{Image: []*image.Paletted{a, b}, Delay: []int{10, 20}, Disposal: []byte{gif.DisposalBackground, gif.DisposalPrevious}, LoopCount: 3}); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestImageImportPreservesGIFAnimationAndWebPWithoutEncoder(t *testing.T) {
	animation := animationFixture(t)
	webp, err := os.ReadFile(filepath.Join("..", "..", "testdata", "media", "lossy-alpha.webp"))
	if err != nil {
		t.Fatal(err)
	}
	c := Converter{CWebP: filepath.Join(t.TempDir(), "encoder-must-not-run")}
	for _, tc := range []struct {
		data []byte
		mime string
	}{{animation, "image/gif"}, {webp, "image/webp"}} {
		data := tc.data
		for round := 0; round < 10; round++ {
			result, kind, err := c.ImportImage(context.Background(), data)
			if err != nil || !result.Preserved || kind != tc.mime || !bytes.Equal(result.Data, tc.data) {
				t.Fatalf("round %d %s: bytes changed: %v", round, kind, err)
			}
			data = result.Data
		}
	}
	decoded, err := gif.DecodeAll(bytes.NewReader(animation))
	if err != nil || len(decoded.Image) != 2 || decoded.Delay[1] != 20 || decoded.LoopCount != 3 || decoded.Disposal[1] != gif.DisposalPrevious {
		t.Fatal("fixture did not contain animation controls", err)
	}
}

func TestImageImportRejectsCorruptLaterGIFFrames(t *testing.T) {
	data := animationFixture(t)
	// The first frame is still decodable; checking only that frame misses damage.
	truncated := data[:len(data)-8]
	if _, err := gif.Decode(bytes.NewReader(truncated)); err != nil {
		t.Fatal("fixture must retain a decodable first frame", err)
	}
	descriptor := []byte{0x2c, 0, 0, 0, 0, 8, 0, 8, 0}
	second := bytes.LastIndex(data, descriptor)
	if second < 0 {
		t.Fatal("fixture missing second frame")
	}
	brokenLZW, outside := bytes.Clone(data), bytes.Clone(data)
	lzw := second + 10
	if packed := data[second+9]; packed&0x80 != 0 {
		lzw += 3 * (1 << ((packed & 7) + 1))
	}
	brokenLZW[lzw] = 0
	outside[second+1], outside[second+2] = 255, 255
	for _, broken := range [][]byte{truncated, data[:len(data)-1], brokenLZW, outside} {
		if _, _, err := (Converter{}).ImportImage(context.Background(), broken); err == nil {
			t.Fatal("damaged animation accepted")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := (Converter{}).ImportImage(ctx, data); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled GIF validation continued", err)
	}
}

func FuzzGIFFrames(f *testing.F) {
	var buf bytes.Buffer
	palette := color.Palette{color.Black, color.White}
	frame := image.NewPaletted(image.Rect(0, 0, 2, 2), palette)
	if err := gif.EncodeAll(&buf, &gif.GIF{Image: []*image.Paletted{frame, frame}, Delay: []int{1, 2}, LoopCount: 0}); err != nil {
		f.Fatal(err)
	}
	f.Add(buf.Bytes())
	f.Add(buf.Bytes()[:len(buf.Bytes())-3])
	f.Add([]byte("GIF89a"))
	f.Fuzz(func(t *testing.T, input []byte) {
		if len(input) > 256<<10 {
			t.Skip()
		}
		// Fuzz container boundaries without spending time decoding huge frames.
		if cfg, err := gif.DecodeConfig(bytes.NewReader(input)); err == nil && int64(cfg.Width)*int64(cfg.Height) > 1_000_000 {
			t.Skip()
		}
		original := bytes.Clone(input)
		_, _ = validateGIF(context.Background(), input)
		if !bytes.Equal(input, original) {
			t.Fatal("GIF validation modified source bytes")
		}
	})
}
