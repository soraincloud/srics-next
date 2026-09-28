package media

import (
	"bytes"
	"image"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

// Exercise our container/metadata parsers with malformed lengths and chunks.
// This intentionally excludes external encoder execution and large pixel work.
func FuzzImageContainers(f *testing.F) {
	for _, name := range []string{"lossless-bare.webp", "lossless-alpha.webp", "lossy-alpha.webp"} {
		b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "media", name))
		if err != nil {
			f.Fatal(err)
		}
		f.Add(b)
	}
	var pngData, jpegData bytes.Buffer
	im := image.NewNRGBA(image.Rect(0, 0, 2, 2))
	if err := png.Encode(&pngData, im); err != nil {
		f.Fatal(err)
	}
	if err := jpeg.Encode(&jpegData, im, nil); err != nil {
		f.Fatal(err)
	}
	f.Add(pngData.Bytes())
	f.Add(jpegData.Bytes())
	f.Add([]byte("RIFF\xff\xff\xff\xffWEBP"))
	f.Fuzz(func(t *testing.T, input []byte) {
		if len(input) > 1<<20 {
			t.Skip()
		}
		before := bytes.Clone(input)
		_, _ = webpChunks(input)
		_, _ = sourceMetadata(input)
		if !bytes.Equal(input, before) {
			t.Fatal("metadata parser changed original bytes")
		}
	})
}
