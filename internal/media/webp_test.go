package media

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

// These committed fixtures make the preservation contract independent of any
// installed encoder, including for WebP that was originally encoded lossily.
func TestWebPRepeatedImportPreservesEveryByteWithoutEncoder(t *testing.T) {
	for _, name := range []string{"lossless-alpha.webp", "lossy-alpha.webp", "lossless-bare.webp"} {
		t.Run(name, func(t *testing.T) {
			original, err := os.ReadFile(filepath.Join("..", "..", "testdata", "media", name))
			if err != nil {
				t.Fatal(err)
			}
			c := Converter{CWebP: filepath.Join(t.TempDir(), "encoder-must-not-run")}
			current := bytes.Clone(original)
			for round := 0; round < 100; round++ {
				res, err := c.Convert(context.Background(), current)
				if err != nil || !res.Preserved || !bytes.Equal(original, res.Data) || sha256.Sum256(original) != sha256.Sum256(res.Data) {
					t.Fatalf("round %d: WebP bytes changed: %v", round, err)
				}
				current = res.Data
			}
		})
	}
}

func TestNonWebPConversionPreservesDecodedPixels(t *testing.T) {
	cwebp, err := exec.LookPath("cwebp")
	if err != nil {
		t.Skip("requires cwebp; full integration job installs libwebp")
	}
	im := image.NewNRGBA(image.Rect(0, 0, 16, 12))
	for y := 0; y < 12; y++ {
		for x := 0; x < 16; x++ {
			im.SetNRGBA(x, y, color.NRGBA{R: byte(x * 17), G: byte(y * 23), B: byte(x*13 + y*7), A: byte((x + y) % 4 * 85)})
		}
	}
	for _, format := range []string{"png", "jpeg"} {
		t.Run(format, func(t *testing.T) {
			var source bytes.Buffer
			var err error
			if format == "png" {
				err = png.Encode(&source, im)
			} else {
				err = jpeg.Encode(&source, im, &jpeg.Options{Quality: 75})
			}
			if err != nil {
				t.Fatal(err)
			}
			decoded, _, err := image.Decode(bytes.NewReader(source.Bytes()))
			if err != nil {
				t.Fatal(err)
			}
			res, err := (Converter{CWebP: cwebp}).Convert(context.Background(), source.Bytes())
			if err != nil || res.Preserved {
				t.Fatalf("conversion: %v", err)
			}
			output, format, err := image.Decode(bytes.NewReader(res.Data))
			if err != nil || format != "webp" || !EqualPixels(decoded, output) {
				t.Fatalf("decoded source pixels changed: %v", err)
			}
			again, err := (Converter{}).Convert(context.Background(), res.Data)
			if err != nil || !again.Preserved || !bytes.Equal(res.Data, again.Data) {
				t.Fatalf("converted WebP changed on reimport: %v", err)
			}
		})
	}
}

func TestNaturalSort(t *testing.T) {
	names := []string{"10.webp", "2.webp", "00002.webp", "1.webp", "999999999999999999999999.webp", "20.webp"}
	NaturalSort(names)
	want := []string{"1.webp", "00002.webp", "2.webp", "10.webp", "20.webp", "999999999999999999999999.webp"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("%v", names)
	}
}
func TestRejectUnsupportedBeforeEncoder(t *testing.T) {
	var p bytes.Buffer
	png.Encode(&p, image.NewNRGBA64(image.Rect(0, 0, 2, 2)))
	for _, input := range [][]byte{[]byte("bad image"), p.Bytes(), bytes.Repeat([]byte{0}, MaxInput+1)} {
		if _, err := (Converter{}).Convert(context.Background(), input); err == nil {
			t.Fatal("unsupported input accepted")
		}
	}
	if dimensions(16384, 1) == nil || dimensions(10000, 10000) == nil {
		t.Fatal("dimension budget ignored")
	}
}
func TestLosslessPNG(t *testing.T) {
	binaryPath, err := exec.LookPath("cwebp")
	if err != nil {
		t.Skip("requires cwebp; full integration job installs libwebp")
	}
	fixture := image.NewNRGBA(image.Rect(0, 0, 8, 5))
	for y := 0; y < 5; y++ {
		for x := 0; x < 8; x++ {
			fixture.SetNRGBA(x, y, color.NRGBA{R: byte(x * 31), G: 91, B: 197, A: byte(y * 51)})
		}
	}
	var source bytes.Buffer
	png.Encode(&source, fixture)
	c := Converter{CWebP: binaryPath}
	res, err := c.Convert(context.Background(), source.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	again, err := c.Convert(context.Background(), res.Data)
	if err != nil || !again.Preserved || !bytes.Equal(res.Data, again.Data) {
		t.Fatal("WebP original changed", err)
	}
	meta := map[string][]byte{"EXIF": []byte("exif-fixture"), "ICCP": []byte("icc-fixture"), "XMP ": []byte("xmp-fixture")}
	muxed, err := muxMetadata(res.Data, meta, 8, 5, true)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Metadata(muxed)
	if err != nil || !reflect.DeepEqual(got, meta) {
		t.Fatal("metadata changed", err)
	}
	preserved, err := (Converter{}).Convert(context.Background(), muxed)
	if err != nil || !preserved.Preserved || !bytes.Equal(preserved.Data, muxed) {
		t.Fatal("WebP containing ICC/EXIF/XMP was rewritten", err)
	}
	// Animation flag alone is sufficient to reject even a decodable bitstream.
	muxed[20] |= 2
	if _, err = c.Convert(context.Background(), muxed); err == nil {
		t.Fatal("animation accepted")
	}
}
func TestWebPContainerBoundaries(t *testing.T) {
	data := append([]byte("RIFF\x00\x00\x00\x00WEBPVP8L"), []byte{255, 255, 255, 127}...)
	binary.LittleEndian.PutUint32(data[4:8], uint32(len(data)-8))
	if _, err := webpChunks(data); err == nil {
		t.Fatal("oversized chunk accepted")
	}
}
