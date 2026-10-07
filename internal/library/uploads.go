package library

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"image"
	_ "image/gif"
	"image/jpeg"
	"io"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/soraincloud/srics-next/internal/media"
	"golang.org/x/image/draw"
)

func (l *Library) Upload(id string) (Upload, error) {
	var up Upload
	if err := l.Check(); err != nil {
		return up, err
	}
	var data []byte
	err := l.db.QueryRow("SELECT data FROM uploads WHERE id=?", id).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return up, ErrMissing
	}
	if err != nil {
		return up, err
	}
	if err = json.Unmarshal(data, &up); err != nil {
		return up, err
	}
	return up, up.validate()
}
func (l *Library) Uploads() ([]Upload, error) {
	if err := l.Check(); err != nil {
		return nil, err
	}
	rows, err := l.db.Query("SELECT data FROM uploads ORDER BY rowid DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Upload{}
	for rows.Next() {
		var b []byte
		var up Upload
		if err = rows.Scan(&b); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(b, &up); err != nil {
			return nil, err
		}
		if err = up.validate(); err != nil {
			return nil, err
		}
		out = append(out, up)
	}
	return out, rows.Err()
}
func (up Upload) validate() error {
	if !IDPattern.MatchString(up.ID) || !ValidModule(up.Module) || up.Module == "novels" || up.Module == "documents" || len(up.Files) < 1 || len(up.Files) > 3000 {
		return errors.New("上传索引损坏，已停止清理，请从备份恢复")
	}
	if up.State != "pending" && up.State != "failed" && up.State != "complete" && up.State != "cancelled" {
		return errors.New("上传状态损坏，已停止清理，请从备份恢复")
	}
	if up.AutoName {
		if _, ok := imageNameNumber(up.Name); !ok || path.Ext(up.Name) != "" || up.Module != "images" || len(up.Files) != 1 || up.Files[0].Name != up.Name {
			return errors.New("图片编号索引损坏，已停止清理")
		}
		if p := up.Files[0].Page; p != nil && (imageNameExtension(p.MIME) == "" || p.Name != up.Name+imageNameExtension(p.MIME)) {
			return errors.New("图片文件名与格式不一致，已停止清理")
		}
	}
	if up.Module == "attachments" {
		if len(up.Files) != 1 || len(up.Tags) != 0 || up.Files[0].Size < 0 || up.Files[0].Size > MaxOrdinaryFile {
			return errors.New("普通文件上传索引损坏，已停止清理")
		}
		f := up.Files[0]
		if f.Page != nil && (f.Page.Size != f.Size || f.Page.MIME != "application/octet-stream" || f.Page.Thumb != "") {
			return errors.New("普通文件原件引用损坏，已停止清理")
		}
	}
	for _, f := range up.Files {
		if f.Page != nil {
			if f.Page.Size == 0 && up.Module != "attachments" {
				return errors.New("上传原件为空，已停止清理")
			}
			if err := f.Page.validate(); err != nil {
				return err
			}
		} else if up.State == "complete" {
			return errors.New("已完成的上传缺少文件引用，已停止清理")
		}
	}
	return nil
}
func (l *Library) saveUpload(up Upload) error {
	data, err := json.Marshal(up)
	if err != nil {
		return err
	}
	_, err = l.db.Exec("INSERT INTO uploads(id,data) VALUES(?,?) ON CONFLICT(id) DO UPDATE SET data=excluded.data", up.ID, data)
	return err
}
func (l *Library) CreateUpload(up Upload) (Upload, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.Check(); err != nil {
		return up, err
	}
	if !IDPattern.MatchString(up.ID) || !ValidModule(up.Module) || up.Module == "novels" || up.Module == "documents" || len(up.Files) == 0 || len(up.Files) > 3000 {
		return up, errors.New("请选择有效的上传内容（每本最多 3000 页）")
	}
	up.Files = append([]UploadFile(nil), up.Files...)
	old, lookupErr := l.Upload(up.ID)
	if lookupErr != nil && !errors.Is(lookupErr, ErrMissing) {
		return up, lookupErr
	}
	// New image imports discard source names before persisting any task. Old
	// unfinished tasks retain their original identity so they can still resume.
	up.AutoName = up.Module == "images" && (errors.Is(lookupErr, ErrMissing) || old.AutoName)
	var imageNumber int64
	if up.AutoName {
		if lookupErr == nil {
			up.Name = old.Name
		} else {
			var e error
			up.Name, imageNumber, e = l.nextImageName()
			if e != nil {
				return up, e
			}
		}
		for i := range up.Files {
			up.Files[i].Name = up.Name
		}
	}
	var err error
	up.Name, err = CleanName(up.Name)
	if err != nil {
		return up, err
	}
	up.Tags, err = CleanTags(up.Tags)
	if err != nil {
		return up, err
	}
	if up.Module != "comics" && (len(up.Files) != 1 || len(up.Tags) != 0) {
		return up, errors.New("每个文件需要独立上传，只有漫画支持多页与标签")
	}
	limit := int64(MaxFile)
	if up.Module == "attachments" {
		limit = MaxOrdinaryFile
	}
	names := map[string]bool{}
	for i, f := range up.Files {
		if f.Name == "" || f.Name == "." || f.Name == ".." || f.Name != path.Base(f.Name) || strings.ContainsAny(f.Name, "\\\x00\r\n") || len(f.Name) > 512 || f.Size < 0 || (f.Size == 0 && up.Module != "attachments") || f.Size > limit || names[f.Name] || (strings.HasPrefix(f.Name, ".") && up.Module != "attachments") {
			if up.Module == "attachments" {
				return up, errors.New("文件名无效或大小超过 10 GiB")
			}
			return up, errors.New("文件名、层级或大小不受支持；单文件上限 64 MiB，同一本不能有重名页面")
		}
		names[f.Name] = true
		up.Files[i].Page = nil
		up.Files[i].SourceHash = ""
	}
	if lookupErr == nil {
		a := old
		b := up
		a.State = ""
		a.Created = ""
		a.Error = ""
		b.State = ""
		b.Created = ""
		b.Error = ""
		for i := range a.Files {
			a.Files[i].Page = nil
			a.Files[i].SourceHash = ""
		}
		aa, _ := json.Marshal(a)
		bb, _ := json.Marshal(b)
		if !bytes.Equal(aa, bb) {
			return up, ErrConflict
		}
		return l.Upload(up.ID)
	}
	up.State = "pending"
	up.Error = ""
	up.Created = time.Now().UTC().Format(time.RFC3339Nano)
	if up.AutoName {
		return up, l.saveNumberedImageUpload(up, imageNumber)
	}
	return up, l.saveUpload(up)
}
func (l *Library) Receive(ctx context.Context, id string, index int, sourceHash string, src io.Reader, c media.Converter) (Upload, error) {
	l.objectsMu.RLock()
	defer l.objectsMu.RUnlock()
	l.mu.Lock()
	defer l.mu.Unlock()
	up, err := l.Upload(id)
	if err != nil {
		return up, err
	}
	if index < 0 || index >= len(up.Files) || len(sourceHash) != 64 {
		return up, errors.New("无效上传页")
	}
	if _, err = hex.DecodeString(sourceHash); err != nil {
		return up, errors.New("无效文件校验值")
	}
	if up.State == "cancelled" {
		return up, errors.New("任务已取消")
	}
	f := &up.Files[index]
	if f.Page != nil {
		if f.SourceHash != sourceHash {
			return up, errors.New("所选文件与原上传内容不同，请重新建立任务")
		}
		return up, nil
	}
	failed := func(e error) (Upload, error) {
		up.State = "failed"
		up.Error = f.Name + "：" + e.Error()
		_ = l.saveUpload(up)
		return up, e
	}
	if up.Module == "attachments" {
		p, e := l.receiveFile(ctx, *f, sourceHash, src)
		if e != nil {
			return failed(e)
		}
		f.Page, f.SourceHash = &p, sourceHash
		up.Error, up.State = "", "pending"
		return up, l.saveUpload(up)
	}
	if err = l.NeedSpace(f.Size * 5); err != nil {
		return failed(err)
	}
	data, err := io.ReadAll(io.LimitReader(src, MaxFile+1))
	if err != nil {
		return failed(errors.New("上传中断，请重新选择来源后继续"))
	}
	if int64(len(data)) != f.Size {
		return failed(errors.New("收到的文件不完整或大小不一致"))
	}
	hash := sha256.Sum256(data)
	if hex.EncodeToString(hash[:]) != sourceHash {
		return failed(errors.New("传输校验失败，请重试"))
	}
	if err = ctx.Err(); err != nil {
		return failed(err)
	}
	mime := http.DetectContentType(data)
	if up.Module == "comics" {
		result, e := c.Convert(ctx, data)
		if e != nil {
			return failed(e)
		}
		data = result.Data
		mime = "image/webp"
	} else if up.Module == "images" {
		if mime != "image/jpeg" && mime != "image/png" && mime != "image/webp" && mime != "image/gif" {
			return failed(errors.New("图片支持 JPEG、PNG、WebP、GIF；其他照片格式可保存到个人照片"))
		}
		cfg, _, e := image.DecodeConfig(bytes.NewReader(data))
		if e != nil || cfg.Width < 1 || cfg.Height < 1 || int64(cfg.Width)*int64(cfg.Height) > media.MaxPixels {
			return failed(errors.New("无法预览此图片或像素数超过 4000 万"))
		}
		if _, _, e = image.Decode(bytes.NewReader(data)); e != nil {
			return failed(errors.New("图片内容损坏，请检查原文件"))
		}
	}
	if err = l.NeedSpace(int64(len(data)) * 2); err != nil {
		return failed(err)
	}
	p, err := l.put(data)
	if err != nil {
		return failed(errors.New("写入失败，请检查资料盘和剩余空间"))
	}
	p.Name = f.Name
	p.MIME = mime
	if up.AutoName {
		p.Name = up.Name + imageNameExtension(mime)
	}
	// Thumbnails are replaceable display copies; source bytes and metadata stay intact.
	// EXIF-oriented images use their original so the browser applies orientation.
	if !hasOrientationMetadata(data) {
		if thumb := thumbnail(data); len(thumb) > 0 {
			tp, e := l.put(thumb)
			if e == nil {
				p.Thumb = tp.Object
			}
		}
	}
	f.Page = &p
	f.SourceHash = sourceHash
	up.Error = ""
	up.State = "pending"
	if err = l.saveUpload(up); err != nil {
		return up, err
	}
	return up, nil
}
func hasOrientationMetadata(data []byte) bool {
	return bytes.Contains(data, []byte("Exif")) || bytes.Contains(data, []byte("EXIF")) || bytes.Contains(data, []byte("eXIf"))
}
func thumbnail(data []byte) []byte {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width < 1 || cfg.Height < 1 || int64(cfg.Width)*int64(cfg.Height) > media.MaxPixels {
		return nil
	}
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil
	}
	w, h := cfg.Width, cfg.Height
	if w > 640 || h > 640 {
		if w >= h {
			h = max(1, h*640/w)
			w = 640
		} else {
			w = max(1, w*640/h)
			h = 640
		}
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(dst, dst.Bounds(), image.White, image.Point{}, draw.Src)
	draw.ApproxBiLinear.Scale(dst, dst.Bounds(), src, src.Bounds(), draw.Over, nil)
	var b bytes.Buffer
	if jpeg.Encode(&b, dst, &jpeg.Options{Quality: 85}) != nil {
		return nil
	}
	return b.Bytes()
}
func (l *Library) Finish(id string) (Item, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	up, err := l.Upload(id)
	if err != nil {
		return Item{}, err
	}
	if up.State == "complete" {
		return l.Item(id)
	}
	if up.State == "cancelled" {
		return Item{}, errors.New("任务已取消")
	}
	byName := map[string]Page{}
	names := []string{}
	for _, f := range up.Files {
		if f.Page == nil {
			return Item{}, errors.New("还有未完成的文件，整本尚未入库")
		}
		names = append(names, f.Name)
		byName[f.Name] = *f.Page
	}
	if up.Module == "comics" {
		media.NaturalSort(names)
	}
	pages := []Page{}
	for _, name := range names {
		pages = append(pages, byName[name])
	}
	encodedPages, _ := json.Marshal(pages)
	encodedTags, _ := json.Marshal(up.Tags)
	up.State = "complete"
	up.Error = ""
	data, _ := json.Marshal(up)
	tx, err := l.db.Begin()
	if err != nil {
		return Item{}, err
	}
	defer tx.Rollback()
	_, err = tx.Exec("INSERT INTO items(id,module,name,tags,created,pages) VALUES(?,?,?,?,?,?)", id, up.Module, up.Name, string(encodedTags), time.Now().UTC().Format(time.RFC3339Nano), string(encodedPages))
	if err != nil {
		return Item{}, err
	}
	if _, err = tx.Exec("UPDATE uploads SET data=? WHERE id=?", data, id); err != nil {
		return Item{}, err
	}
	if err = tx.Commit(); err != nil {
		return Item{}, err
	}
	return l.Item(id)
}
func (l *Library) Cancel(id string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	up, err := l.Upload(id)
	if err != nil {
		return err
	}
	if up.State == "complete" {
		return errors.New("已完成的内容请从资料库移到回收站")
	}
	up.State = "cancelled"
	if _, err := l.db.Exec("DELETE FROM transfers WHERE private=0 AND json_extract(payload,'$.parent')=?", id); err != nil {
		return err
	}
	return l.saveUpload(up)
}
