package media

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image/gif"
	"net/http"
)

// ImportImage preserves WebP and GIF bytes. Other supported static images use
// the same lossless, pixel-verified converter as comic pages.
func (c Converter) ImportImage(ctx context.Context, input []byte) (Result, string, error) {
	if len(input) > MaxInput {
		return Result{}, "", errors.New("单张图片不能超过 64 MiB")
	}
	switch http.DetectContentType(input) {
	case "image/gif":
		result, err := validateGIF(ctx, input)
		return result, "image/gif", err
	case "image/webp", "image/png", "image/jpeg":
		result, err := c.Convert(ctx, input)
		return result, "image/webp", err
	default:
		return Result{}, "", errors.New("图片支持 JPEG、PNG、WebP 和 GIF；其他格式可保存到个人照片")
	}
}

// Check every GIF frame without retaining all decoded frames in memory. A
// first-frame-only decode would accept a truncated or corrupt later frame.
func validateGIF(ctx context.Context, input []byte) (Result, error) {
	invalid := errors.New("GIF 内容损坏或不完整，请检查原文件")
	cfg, err := gif.DecodeConfig(bytes.NewReader(input))
	if err != nil || cfg.Width < 1 || cfg.Height < 1 || int64(cfg.Width)*int64(cfg.Height) > MaxPixels || len(input) < 13 {
		return Result{}, errors.New("GIF 无法解码或画布像素数超过 4000 万")
	}
	headerEnd := 13
	if input[10]&0x80 != 0 {
		headerEnd += 3 * (1 << ((input[10] & 7) + 1))
	}
	if headerEnd > len(input) {
		return Result{}, invalid
	}
	off, extensions, frames := headerEnd, headerEnd, 0
	var pixels int64
	// Count compressed sub-blocks, checking lengths before any allocation.
	skipBlocks := func() bool {
		for off < len(input) {
			n := int(input[off])
			off++
			if n == 0 {
				return true
			}
			if n > len(input)-off {
				return false
			}
			off += n
		}
		return false
	}
	for off < len(input) {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		switch input[off] {
		case 0x21: // Extension: retain it for the following frame's decoder.
			off++
			if off >= len(input) {
				return Result{}, invalid
			}
			label := input[off]
			off++
			if label == 0xf9 && (len(input)-off < 6 || input[off] != 4 || input[off+5] != 0) {
				return Result{}, invalid
			}
			if !skipBlocks() {
				return Result{}, invalid
			}
		case 0x2c: // Image descriptor, optional palette, LZW data.
			if len(input)-off < 10 {
				return Result{}, invalid
			}
			d := input[off+1 : off+10]
			x, y := int(binary.LittleEndian.Uint16(d)), int(binary.LittleEndian.Uint16(d[2:]))
			w, h := int(binary.LittleEndian.Uint16(d[4:])), int(binary.LittleEndian.Uint16(d[6:]))
			if w < 1 || h < 1 || x+w > cfg.Width || y+h > cfg.Height {
				return Result{}, invalid
			}
			pixels += int64(w) * int64(h)
			if pixels > 10*MaxPixels {
				return Result{}, errors.New("GIF 全部帧的总像素数超过 4 亿，请缩短动画后上传")
			}
			off += 10
			if d[8]&0x80 != 0 {
				off += 3 * (1 << ((d[8] & 7) + 1))
			}
			if off >= len(input) {
				return Result{}, invalid
			}
			off++ // LZW minimum code size; checked by the standard decoder.
			if !skipBlocks() {
				return Result{}, invalid
			}
			frame := bytes.NewBuffer(make([]byte, 0, headerEnd+off-extensions+1))
			frame.Write(input[:headerEnd])
			frame.Write(input[extensions:off])
			frame.WriteByte(0x3b)
			if _, err := gif.Decode(frame); err != nil {
				return Result{}, invalid
			}
			frames++
			extensions = off
		case 0x3b:
			if frames == 0 || off+1 != len(input) {
				return Result{}, invalid
			}
			return Result{Data: bytes.Clone(input), Preserved: true, Width: cfg.Width, Height: cfg.Height}, nil
		default:
			return Result{}, invalid
		}
	}
	return Result{}, invalid
}
