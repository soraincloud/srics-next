// Package media defines the canonical decode contract for static images:
// Go's JPEG/PNG decoder, no color transform or EXIF rotation, then lossless
// WebP encoding. ICC/EXIF/XMP are preserved separately, without double rotation.
package media

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	_ "image/jpeg"
	"image/png"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/image/webp"
)

const MaxInput = 64 << 20
const MaxPixels = 40_000_000

type Result struct {
	Data          []byte
	Preserved     bool
	Width, Height int
}
type Converter struct{ CWebP string }

func (c Converter) Convert(ctx context.Context, input []byte) (Result, error) {
	var result Result
	if len(input) > MaxInput {
		return result, errors.New("image exceeds 64 MiB verification limit")
	}
	if len(input) >= 12 && string(input[:4]) == "RIFF" && string(input[8:12]) == "WEBP" {
		chunks, err := webpChunks(input)
		if err != nil {
			return result, err
		}
		for _, p := range chunks {
			if p.kind == "ANIM" || p.kind == "ANMF" || (p.kind == "VP8X" && len(p.data) > 0 && p.data[0]&2 != 0) {
				return result, errors.New("animated WebP is unsupported")
			}
		}
		cfg, err := webp.DecodeConfig(bytes.NewReader(input))
		if err != nil {
			return result, err
		}
		if err = dimensions(cfg.Width, cfg.Height); err != nil {
			return result, err
		}
		if _, err = webp.Decode(bytes.NewReader(input)); err != nil {
			return result, err
		}
		return Result{Data: bytes.Clone(input), Preserved: true, Width: cfg.Width, Height: cfg.Height}, nil
	}
	meta, err := sourceMetadata(input)
	if err != nil {
		return result, err
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(input))
	if err != nil {
		return result, err
	}
	if err = dimensions(cfg.Width, cfg.Height); err != nil {
		return result, err
	}
	source, _, err := image.Decode(bytes.NewReader(input))
	if err != nil {
		return result, err
	}
	canonical := image.NewNRGBA(image.Rect(0, 0, cfg.Width, cfg.Height))
	alpha := false
	for y := 0; y < cfg.Height; y++ {
		for x := 0; x < cfg.Width; x++ {
			p := color.NRGBAModel.Convert(source.At(x, y)).(color.NRGBA)
			canonical.SetNRGBA(x, y, p)
			alpha = alpha || p.A != 255
		}
	}
	// Only ordinary comic pages and images reach this converter. Private photos retain
	// original bytes and must never be routed through these plaintext temp files.
	dir, err := os.MkdirTemp("", "srics-webp-")
	if err != nil {
		return result, err
	}
	defer os.RemoveAll(dir)
	inPath, outPath := filepath.Join(dir, "canonical.png"), filepath.Join(dir, "page.webp")
	f, err := os.OpenFile(inPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return result, err
	}
	err = png.Encode(f, canonical)
	closeErr := f.Close()
	if err != nil {
		return result, err
	}
	if closeErr != nil {
		return result, closeErr
	}
	cmd := exec.CommandContext(ctx, c.CWebP, "-quiet", "-lossless", "-exact", "-m", "4", inPath, "-o", outPath)
	if err = cmd.Run(); err != nil {
		return result, fmt.Errorf("lossless encoder failed: %w", err)
	}
	encoded, err := os.ReadFile(outPath)
	if err != nil {
		return result, err
	}
	encoded, err = muxMetadata(encoded, meta, cfg.Width, cfg.Height, alpha)
	if err != nil {
		return result, err
	}
	decoded, err := webp.Decode(bytes.NewReader(encoded))
	if err != nil {
		return result, err
	}
	if !EqualPixels(canonical, decoded) {
		return result, errors.New("lossless pixel verification failed")
	}
	return Result{Data: encoded, Width: cfg.Width, Height: cfg.Height}, nil
}
func dimensions(w, h int) error {
	if w < 1 || h < 1 || w > 16383 || h > 16383 || int64(w)*int64(h) > MaxPixels {
		return errors.New("unsupported image dimensions or pixel budget")
	}
	return nil
}
func EqualPixels(a, b image.Image) bool {
	if a.Bounds() != b.Bounds() {
		return false
	}
	for y := 0; y < a.Bounds().Dy(); y++ {
		for x := 0; x < a.Bounds().Dx(); x++ {
			if color.NRGBAModel.Convert(a.At(x, y)) != color.NRGBAModel.Convert(b.At(x, y)) {
				return false
			}
		}
	}
	return true
}

type chunk struct {
	kind string
	data []byte
}

func webpChunks(data []byte) ([]chunk, error) {
	if len(data) < 12 || string(data[:4]) != "RIFF" || string(data[8:12]) != "WEBP" || uint64(binary.LittleEndian.Uint32(data[4:8]))+8 != uint64(len(data)) {
		return nil, errors.New("invalid WebP container")
	}
	var chunks []chunk
	for off := 12; off < len(data); {
		if off+8 > len(data) {
			return nil, errors.New("truncated WebP chunk")
		}
		size := int(binary.LittleEndian.Uint32(data[off+4 : off+8]))
		end := off + 8 + size
		if end < off || end+(size&1) > len(data) {
			return nil, errors.New("invalid WebP chunk size")
		}
		chunks = append(chunks, chunk{string(data[off : off+4]), data[off+8 : end]})
		off = end + (size & 1)
	}
	return chunks, nil
}
func Metadata(data []byte) (map[string][]byte, error) {
	chunks, err := webpChunks(data)
	if err != nil {
		return nil, err
	}
	meta := map[string][]byte{}
	for _, p := range chunks {
		if p.kind == "ICCP" || p.kind == "EXIF" || p.kind == "XMP " {
			meta[p.kind] = bytes.Clone(p.data)
		}
	}
	return meta, nil
}
func muxMetadata(data []byte, meta map[string][]byte, w, h int, alpha bool) ([]byte, error) {
	if len(meta) == 0 {
		return data, nil
	}
	chunks, err := webpChunks(data)
	if err != nil {
		return nil, err
	}
	var payload bytes.Buffer
	payload.WriteString("WEBP")
	add := func(kind string, p []byte) {
		payload.WriteString(kind)
		binary.Write(&payload, binary.LittleEndian, uint32(len(p)))
		payload.Write(p)
		if len(p)%2 != 0 {
			payload.WriteByte(0)
		}
	}
	flags := byte(0)
	if len(meta["ICCP"]) > 0 {
		flags |= 0x20
	}
	if len(meta["EXIF"]) > 0 {
		flags |= 8
	}
	if len(meta["XMP "]) > 0 {
		flags |= 4
	}
	if alpha {
		flags |= 0x10
	}
	x := []byte{flags, 0, 0, 0, byte(w - 1), byte((w - 1) >> 8), byte((w - 1) >> 16), byte(h - 1), byte((h - 1) >> 8), byte((h - 1) >> 16)}
	add("VP8X", x)
	if p := meta["ICCP"]; len(p) > 0 {
		add("ICCP", p)
	}
	for _, p := range chunks {
		if p.kind != "VP8X" && p.kind != "ICCP" && p.kind != "EXIF" && p.kind != "XMP " {
			add(p.kind, p.data)
		}
	}
	for _, kind := range []string{"EXIF", "XMP "} {
		if p := meta[kind]; len(p) > 0 {
			add(kind, p)
		}
	}
	var out bytes.Buffer
	out.WriteString("RIFF")
	binary.Write(&out, binary.LittleEndian, uint32(payload.Len()))
	out.Write(payload.Bytes())
	return out.Bytes(), nil
}

func sourceMetadata(data []byte) (map[string][]byte, error) {
	meta := map[string][]byte{}
	if bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")) {
		for off := 8; off < len(data); {
			if off+12 > len(data) {
				return nil, errors.New("truncated PNG")
			}
			size := int(binary.BigEndian.Uint32(data[off : off+4]))
			end := off + 8 + size
			if end < off || end+4 > len(data) {
				return nil, errors.New("invalid PNG chunk")
			}
			kind := string(data[off+4 : off+8])
			p := data[off+8 : end]
			if crc32.ChecksumIEEE(data[off+4:end]) != binary.BigEndian.Uint32(data[end:end+4]) {
				return nil, errors.New("invalid PNG checksum")
			}
			switch kind {
			case "IHDR":
				if len(p) != 13 || p[8] == 16 {
					return nil, errors.New("16-bit PNG is unsupported")
				}
			case "acTL", "fcTL", "fdAT":
				return nil, errors.New("animated PNG is unsupported")
			case "eXIf":
				meta["EXIF"] = bytes.Clone(p)
			case "iCCP":
				i := bytes.IndexByte(p, 0)
				if i < 1 || i+2 >= len(p) || p[i+1] != 0 {
					return nil, errors.New("invalid ICC profile")
				}
				r, e := zlib.NewReader(bytes.NewReader(p[i+2:]))
				if e != nil {
					return nil, e
				}
				profile, e := io.ReadAll(io.LimitReader(r, 4<<20+1))
				r.Close()
				if e != nil || len(profile) > 4<<20 {
					return nil, errors.New("invalid or oversized ICC profile")
				}
				meta["ICCP"] = profile
			case "iTXt":
				if bytes.HasPrefix(p, []byte("XML:com.adobe.xmp\x00")) {
					parts := bytes.SplitN(p, []byte{0}, 6)
					if len(parts) != 6 || len(parts[1]) != 0 {
						return nil, errors.New("compressed XMP is unsupported")
					}
					meta["XMP "] = bytes.Clone(parts[5])
				}
			}
			off = end + 4
		}
		return meta, nil
	}
	if !bytes.HasPrefix(data, []byte{0xff, 0xd8}) {
		return nil, errors.New("only static 8-bit PNG, JPEG and WebP are supported")
	}
	icc := map[int][]byte{}
	iccTotal := 0
	for off := 2; off < len(data); {
		if data[off] != 0xff {
			return nil, errors.New("invalid JPEG marker")
		}
		for off < len(data) && data[off] == 0xff {
			off++
		}
		if off >= len(data) {
			return nil, errors.New("truncated JPEG")
		}
		marker := data[off]
		off++
		if marker == 0xda || marker == 0xd9 {
			break
		}
		if off+2 > len(data) {
			return nil, errors.New("truncated JPEG segment")
		}
		size := int(binary.BigEndian.Uint16(data[off : off+2]))
		if size < 2 || off+size > len(data) {
			return nil, errors.New("invalid JPEG segment")
		}
		p := data[off+2 : off+size]
		off += size
		if (marker == 0xc0 || marker == 0xc2) && (len(p) == 0 || p[0] != 8) {
			return nil, errors.New("non-8-bit JPEG is unsupported")
		}
		if marker == 0xe1 && bytes.HasPrefix(p, []byte("Exif\x00\x00")) {
			meta["EXIF"] = bytes.Clone(p[6:])
		}
		const xmp = "http://ns.adobe.com/xap/1.0/\x00"
		if marker == 0xe1 && bytes.HasPrefix(p, []byte(xmp)) {
			meta["XMP "] = bytes.Clone(p[len(xmp):])
		}
		if marker == 0xe2 && bytes.HasPrefix(p, []byte("ICC_PROFILE\x00")) {
			if len(p) < 14 || p[12] == 0 || p[13] == 0 {
				return nil, errors.New("invalid JPEG ICC segment")
			}
			n, total := int(p[12]), int(p[13])
			if n > total || icc[n] != nil || (iccTotal != 0 && total != iccTotal) {
				return nil, errors.New("inconsistent JPEG ICC segments")
			}
			iccTotal = total
			icc[n] = p[14:]
		}
	}
	if iccTotal > 0 {
		if len(icc) != iccTotal {
			return nil, errors.New("incomplete JPEG ICC profile")
		}
		for n := 1; n <= iccTotal; n++ {
			meta["ICCP"] = append(meta["ICCP"], icc[n]...)
		}
	}
	return meta, nil
}

// NaturalSort compares digit runs without parsing into a bounded integer.
func NaturalSort(names []string) {
	sort.SliceStable(names, func(i, j int) bool { return naturalLess(names[i], names[j]) })
}
func naturalLess(a, b string) bool {
	x, y := strings.ToLower(a), strings.ToLower(b)
	i, j := 0, 0
	for i < len(x) && j < len(y) {
		if x[i] >= '0' && x[i] <= '9' && y[j] >= '0' && y[j] <= '9' {
			ii, jj := i, j
			for i < len(x) && x[i] >= '0' && x[i] <= '9' {
				i++
			}
			for j < len(y) && y[j] >= '0' && y[j] <= '9' {
				j++
			}
			xx, yy := strings.TrimLeft(x[ii:i], "0"), strings.TrimLeft(y[jj:j], "0")
			if len(xx) != len(yy) {
				return len(xx) < len(yy)
			}
			if xx != yy {
				return xx < yy
			}
		} else {
			if x[i] != y[j] {
				return x[i] < y[j]
			}
			i++
			j++
		}
	}
	if i != len(x) || j != len(y) {
		return i == len(x)
	}
	return a < b
}
