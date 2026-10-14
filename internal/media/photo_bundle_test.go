package media

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"image"
	"image/jpeg"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPhotoBundleKeepsBothOriginalsAndRejectsUnsafeArchives(t *testing.T) {
	image, err := os.ReadFile(filepath.Join("..", "..", "testdata", "media", "synthetic-photo.heic"))
	if err != nil {
		t.Fatal(err)
	}
	movie, err := os.ReadFile(filepath.Join("..", "..", "testdata", "media", "synthetic-photo.MOV"))
	if err != nil {
		t.Fatal(err)
	}
	makeBundle := func(imageName, movieName string, method uint16, extra bool) []byte {
		var out bytes.Buffer
		z := zip.NewWriter(&out)
		for _, part := range []struct {
			name string
			data []byte
		}{{imageName, image}, {movieName, movie}} {
			w, _ := z.CreateHeader(&zip.FileHeader{Name: part.name, Method: method})
			w.Write(part.data)
		}
		if extra {
			w, _ := z.Create("extra.txt")
			w.Write([]byte("extra"))
		}
		z.Close()
		return out.Bytes()
	}
	data := makeBundle("synthetic-photo.heic", "synthetic-photo.MOV", zip.Store, false)
	bundle, err := ReadPhotoBundle(data)
	if err != nil || !bytes.Equal(bundle.Image, image) || !bytes.Equal(bundle.Motion, movie) {
		t.Fatal("originals changed", err)
	}
	bundle.Clear()
	for _, names := range [][2]string{{"caf\u00e9.heic", "cafe\u0301.MOV"}, {"\u0130.heic", "i\u0307.MOV"}, {"\u039f\u03a3.heic", "\u03bf\u03c2.MOV"}} {
		bundle, err = ReadPhotoBundle(makeBundle(names[0], names[1], zip.Store, false))
		if err != nil || bundle.ImageName != names[0] || bundle.MotionName != names[1] || !bytes.Equal(bundle.Image, image) || !bytes.Equal(bundle.Motion, movie) {
			t.Fatal("equivalent Unicode names failed or originals changed", names, err)
		}
		bundle.Clear()
	}
	broken := bytes.Clone(data)
	broken[bytes.Index(broken, image)+10] ^= 1
	for _, bad := range [][]byte{data[:len(data)-12], broken, makeBundle("../synthetic-photo.heic", "synthetic-photo.MOV", zip.Store, false), makeBundle("other.heic", "synthetic-photo.MOV", zip.Store, false), makeBundle("synthetic-photo.heic", "synthetic-photo.MOV", zip.Deflate, false), makeBundle("synthetic-photo.heic", "synthetic-photo.MOV", zip.Store, true)} {
		if _, err := ReadPhotoBundle(bad); err == nil {
			t.Fatal("unsafe/corrupt photo bundle accepted")
		}
	}
}

func TestStaticConverterRejectsEmbeddedMotionPhoto(t *testing.T) {
	var data bytes.Buffer
	jpeg.Encode(&data, image.NewNRGBA(image.Rect(0, 0, 2, 2)), nil)
	data.WriteString("Camera:MotionPhoto=1; synthetic video payload")
	if _, err := (Converter{}).Convert(context.Background(), data.Bytes()); err == nil || !strings.Contains(err.Error(), "动态照片") {
		t.Fatal("motion photo converted to still image")
	}
}

func TestPhotoBundleRejectsExcessiveCatalogBeforeAllocatingEntries(t *testing.T) {
	// Lie about the count in the footer while supplying thousands of central
	// records. Reject the archive before archive/zip allocates one File per record.
	const records = 50000
	data := make([]byte, records*47+22)
	for off := 0; off < records*47; off += 47 {
		binary.LittleEndian.PutUint32(data[off:], 0x02014b50)
		binary.LittleEndian.PutUint16(data[off+28:], 1)
		data[off+46] = 'x'
	}
	footer := data[len(data)-22:]
	binary.LittleEndian.PutUint32(footer, 0x06054b50)
	binary.LittleEndian.PutUint16(footer[8:], 2)
	binary.LittleEndian.PutUint16(footer[10:], 2)
	binary.LittleEndian.PutUint32(footer[12:], records*47)
	allocations := testing.AllocsPerRun(1, func() {
		if _, err := ReadPhotoBundle(data); err == nil {
			t.Fatal("dishonest directory accepted")
		}
	})
	if allocations > 1000 {
		t.Fatalf("dishonest directory forced %.0f allocations before rejection", allocations)
	}
	t.Logf("dishonest 50,000-entry directory rejected with %.0f allocations", allocations)
}
