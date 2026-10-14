package media

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"path"
	"strings"

	"golang.org/x/text/cases"
	"golang.org/x/text/language"
	"golang.org/x/text/unicode/norm"
)

const PhotoBundleMIME = "application/x-srics-live-photo"

type PhotoBundle struct {
	Image, Motion         []byte
	ImageName, MotionName string
}

func (b *PhotoBundle) Clear() { clear(b.Image); clear(b.Motion) }

// A Live Photo is one item whose original is an uncompressed, two-file ZIP.
// Both original filenames and all file bytes survive exports and recovery.
func ReadPhotoBundle(data []byte) (result PhotoBundle, err error) {
	defer func() {
		if err != nil {
			result.Clear()
		}
	}()
	if len(data) > MaxInput {
		return result, errors.New("实况照片两部分合计最多 64 MiB")
	}
	// These app-generated containers have exactly two files, no ZIP64, comment
	// or split volumes. Bound the actual catalog before archive/zip allocates
	// entries: its footer count alone is not a safe allocation limit.
	if !boundedPhotoCatalog(data) {
		return result, errors.New("实况照片文件目录无效")
	}
	z, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil || len(z.File) != 2 {
		return result, errors.New("实况照片需包含一张原始照片和一个配套 MOV 视频")
	}
	for _, file := range z.File {
		if file.Name != path.Base(file.Name) || file.Name == "." || strings.ContainsAny(file.Name, "\\\x00\r\n") || len(file.Name) > 512 || !file.Mode().IsRegular() || file.Method != zip.Store || file.Flags&1 != 0 || file.CompressedSize64 != file.UncompressedSize64 || file.UncompressedSize64 > MaxInput || file.UncompressedSize64 == 0 {
			return result, errors.New("实况照片文件结构无效")
		}
		r, e := file.Open()
		if e != nil {
			return result, e
		}
		body, e := io.ReadAll(io.LimitReader(r, MaxInput+1))
		closeErr := r.Close()
		if e != nil || closeErr != nil || len(body) != int(file.UncompressedSize64) {
			clear(body)
			return result, errors.New("实况照片原文件校验失败")
		}
		if strings.EqualFold(path.Ext(file.Name), ".mov") {
			if result.Motion != nil || len(body) < 12 || string(body[4:8]) != "ftyp" || SupportedImage(ImageMIME(body)) {
				clear(body)
				return result, errors.New("配套视频不是有效 MOV 文件")
			}
			result.Motion, result.MotionName = body, file.Name
		} else {
			kind := ImageMIME(body)
			if result.Image != nil || (kind != "image/heic" && kind != "image/heif" && kind != "image/jpeg" && kind != "image/png") {
				clear(body)
				return result, errors.New("实况照片的静态原件格式无效")
			}
			result.Image, result.ImageName = body, file.Name
		}
	}
	// Match the browser's NFC + Unicode lowercase key while keeping both names
	// byte-for-byte in the archive. Casers are instantiated per call for safety.
	stem := func(name string) string {
		return cases.Lower(language.Und).String(norm.NFC.String(strings.TrimSuffix(name, path.Ext(name))))
	}
	if result.Image == nil || result.Motion == nil || stem(result.ImageName) != stem(result.MotionName) {
		return result, errors.New("实况照片与 MOV 视频的文件名称需要对应")
	}
	return result, nil
}

func boundedPhotoCatalog(data []byte) bool {
	if len(data) < 22 {
		return false
	}
	end := len(data) - 22
	footer := data[end:]
	if binary.LittleEndian.Uint32(footer) != 0x06054b50 ||
		binary.LittleEndian.Uint16(footer[4:]) != 0 ||
		binary.LittleEndian.Uint16(footer[6:]) != 0 ||
		binary.LittleEndian.Uint16(footer[8:]) != 2 ||
		binary.LittleEndian.Uint16(footer[10:]) != 2 ||
		binary.LittleEndian.Uint16(footer[20:]) != 0 {
		return false
	}
	size := uint64(binary.LittleEndian.Uint32(footer[12:]))
	offset := uint64(binary.LittleEndian.Uint32(footer[16:]))
	return size >= 92 && size <= 4096 && offset <= uint64(end) && size == uint64(end)-offset
}
