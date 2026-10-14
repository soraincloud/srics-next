package media

import (
	"archive/zip"
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

func FuzzPhotoBundle(f *testing.F) {
	var archive bytes.Buffer
	z := zip.NewWriter(&archive)
	for _, ext := range []string{"heic", "MOV"} {
		data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "media", "synthetic-photo."+ext))
		if err != nil {
			f.Fatal(err)
		}
		w, err := z.CreateHeader(&zip.FileHeader{Name: "synthetic-photo." + ext, Method: zip.Store})
		if err != nil {
			f.Fatal(err)
		}
		if _, err := w.Write(data); err != nil {
			f.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		f.Fatal(err)
	}
	f.Add(archive.Bytes())
	f.Add([]byte("PK\x05\x06"))
	f.Fuzz(func(t *testing.T, input []byte) {
		if len(input) > 1<<20 {
			t.Skip()
		}
		before := bytes.Clone(input)
		bundle, _ := ReadPhotoBundle(input)
		bundle.Clear()
		if !bytes.Equal(input, before) {
			t.Fatal("photo parser or clear changed original bytes")
		}
	})
}
