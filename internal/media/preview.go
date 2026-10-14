package media

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"image/png"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"
)

const MaxPreviewEdge = 2560

type PreviewResult struct {
	MIME      string `json:"mime"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	Data      []byte `json:"-"`
	Depth     int    `json:"depth"`
	Images    int    `json:"images"`
	Auxiliary bool   `json:"auxiliary"`
}

// Serialize native decoders across uploads and views, with cancellable waits.
var previewSlot = make(chan struct{}, 1)

func nativeTool() string {
	if executable, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(executable), "tools", "srics-image-preview")
		if info, err := os.Stat(candidate); err == nil && info.Mode().IsRegular() {
			return candidate
		}
	}
	tool, _ := exec.LookPath("srics-image-preview")
	return tool
}

type limitedOutput struct {
	bytes.Buffer
	limit int
}

func (w *limitedOutput) Write(p []byte) (int, error) {
	if len(p) > w.limit-w.Len() {
		return 0, errors.New("preview output exceeds limit")
	}
	return w.Buffer.Write(p)
}

// Preview downscales only the display copy. No original bytes or metadata are
// rewritten; stdin/stdout keep private photo content off the filesystem.
func (c Converter) Preview(ctx context.Context, input []byte, edge int) (PreviewResult, error) {
	var result PreviewResult
	if len(input) == 0 || len(input) > MaxInput || edge < 0 || edge > MaxPreviewEdge {
		return result, errors.New("图片预览输入超出限制")
	}
	tool := c.NativePreview
	if tool == "" {
		tool = nativeTool()
	}
	if tool == "" {
		return result, errors.New("缺少图片预览组件，请使用完整 macOS 应用或重新构建")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	select {
	case previewSlot <- struct{}{}:
		defer func() { <-previewSlot }()
	case <-ctx.Done():
		return result, ctx.Err()
	}
	cmd := exec.CommandContext(ctx, tool, strconv.Itoa(edge), strconv.Itoa(MaxPixels))
	cmd.WaitDelay = 2 * time.Second
	cmd.Stdin = bytes.NewReader(input)
	outputLimit := 32 << 20
	if edge == 0 {
		outputLimit = 512 << 20
	}
	out, stderr := &limitedOutput{limit: outputLimit + 4096}, &limitedOutput{limit: 4096}
	cmd.Stdout, cmd.Stderr = out, stderr
	defer clear(stderr.Bytes())
	if err := cmd.Run(); err != nil {
		clear(out.Bytes())
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		if bytes.Contains(stderr.Bytes(), []byte("preserve original")) {
			return result, errors.New("图片含高位深、HDR 或辅助图像，请上传到个人照片保留原件")
		}
		return result, errors.New("图片无法解码或超过 1 亿像素，请检查原文件")
	}
	data := out.Bytes()
	newline := bytes.IndexByte(data, '\n')
	if newline < 1 || newline > 4096 || json.Unmarshal(data[:newline], &result) != nil || CheckPixels(result.Width, result.Height) != nil || !SupportedImage(result.MIME) {
		clear(data)
		return PreviewResult{}, errors.New("图片预览组件返回无效数据")
	}
	result.Data = data[newline+1:]
	cfg, err := png.DecodeConfig(bytes.NewReader(result.Data))
	if err != nil || CheckPixels(cfg.Width, cfg.Height) != nil || (edge > 0 && (cfg.Width > edge || cfg.Height > edge)) {
		clear(data)
		return PreviewResult{}, errors.New("图片预览尺寸无效")
	}
	return result, nil
}

func IsMotionPhoto(input []byte) bool {
	return bytes.Contains(input, []byte("MotionPhoto")) || bytes.Contains(input, []byte("MicroVideo"))
}

// Ordinary collections may convert only uncomplicated 8-bit static images.
// HDR, auxiliary images and motion must remain originals in the photo modules.
func (c Converter) ConvertNative(ctx context.Context, input []byte) (Result, error) {
	if IsMotionPhoto(input) {
		return Result{}, errors.New("动态照片请上传到个人照片以保留原件")
	}
	info, err := c.Preview(ctx, input, 0)
	if err != nil {
		return Result{}, err
	}
	defer clear(info.Data)
	if info.Depth < 1 || info.Depth > 8 || info.Images != 1 || info.Auxiliary {
		return Result{}, errors.New("图片含高位深、HDR 或辅助图像，请上传到个人照片保留原件")
	}
	return c.convert(ctx, info.Data, 512<<20)
}

func SupportedImage(kind string) bool {
	switch kind {
	case "image/jpeg", "image/png", "image/webp", "image/gif", "image/heic", "image/heif", "image/avif", "image/bmp", "image/tiff":
		return true
	}
	return false
}

func NeedsNativePreview(kind string) bool {
	return kind == "image/heic" || kind == "image/heif" || kind == "image/avif" || kind == "image/bmp" || kind == "image/tiff"
}

// Detect by bytes rather than a filename supplied by the browser. ISO BMFF
// compatible brands are bounded by the ftyp box, not searched in pixel data.
func ImageMIME(input []byte) string {
	if bytes.HasPrefix(input, []byte("II\x2a\x00")) || bytes.HasPrefix(input, []byte("MM\x00\x2a")) {
		return "image/tiff"
	}
	if len(input) >= 16 && string(input[4:8]) == "ftyp" {
		n := uint64(binary.BigEndian.Uint32(input[:4]))
		if n >= 16 && n <= uint64(len(input)) && n <= 4096 && n%4 == 0 {
			heic, heif := false, false
			for i := 8; i < int(n); i += 4 {
				if i == 12 { // minor version is not a brand
					continue
				}
				switch string(input[i : i+4]) {
				case "avif", "avis":
					return "image/avif"
				case "heic", "heix", "hevc", "hevx", "heim", "heis":
					heic = true
				case "mif1", "msf1":
					heif = true
				}
			}
			if heic {
				return "image/heic"
			}
			if heif {
				return "image/heif"
			}
		}
	}
	return http.DetectContentType(input)
}
