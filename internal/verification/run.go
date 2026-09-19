// Package verification runs a destructive-to-its-own-fixtures-only recovery
// rehearsal. It never accepts user files, cloud credentials or an existing store.
package verification

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"filippo.io/age"
	"github.com/soraincloud/srics-next/internal/backup"
	"github.com/soraincloud/srics-next/internal/media"
	"github.com/soraincloud/srics-next/internal/store"
	"github.com/soraincloud/srics-next/internal/vault"
)

type Check struct {
	ID         string `json:"id"`
	Label      string `json:"label"`
	Status     string `json:"status"`
	Detail     string `json:"detail,omitempty"`
	DurationMS int64  `json:"durationMs"`
}
type Report struct {
	Status     string            `json:"status"`
	StartedAt  time.Time         `json:"startedAt"`
	FinishedAt *time.Time        `json:"finishedAt,omitempty"`
	Versions   map[string]string `json:"versions"`
	Checks     []Check           `json:"checks"`
}
type Tools struct{ CWebP, Restic string }

var labels = [][2]string{{"tools", "运行依赖"}, {"pixels", "无损 WebP 与元数据"}, {"formats", "原件保留与格式拒绝"}, {"encryption", "文件与名称加密"}, {"tamper", "错误口令与篡改拒绝"}, {"storage", "不可变文件与加密索引"}, {"privacy", "落盘明文检查"}, {"snapshot", "SQLite 一致性快照"}, {"backup", "锁定状态下加密备份"}, {"check", "备份数据完整性"}, {"restore", "移除源数据后恢复"}}

func Pending() Report {
	r := Report{Status: "idle", Versions: map[string]string{}}
	for _, l := range labels {
		r.Checks = append(r.Checks, Check{ID: l[0], Label: l[1], Status: "pending"})
	}
	return r
}
func ResolveTools() Tools { return Tools{CWebP: findTool("cwebp"), Restic: findTool("restic")} }
func findTool(name string) string {
	exe, err := os.Executable()
	if err == nil {
		path := filepath.Join(filepath.Dir(exe), "tools", name)
		if info, e := os.Stat(path); e == nil && info.Mode().IsRegular() && info.Mode()&0111 != 0 {
			return path
		}
	}
	p, _ := exec.LookPath(name)
	return p
}

func Run(ctx context.Context, tools Tools, publish func(Report)) Report {
	r := Pending()
	r.Status = "running"
	r.StartedAt = time.Now().UTC()
	emit := func() {
		if publish != nil {
			copyR := r
			copyR.Checks = append([]Check(nil), r.Checks...)
			copyR.Versions = map[string]string{}
			for k, v := range r.Versions {
				copyR.Versions[k] = v
			}
			publish(copyR)
		}
	}
	finish := func() Report { now := time.Now().UTC(); r.FinishedAt = &now; emit(); return r }
	step := func(index int, fn func() (string, error)) bool {
		r.Checks[index].Status = "running"
		emit()
		start := time.Now()
		detail, err := fn()
		r.Checks[index].DurationMS = time.Since(start).Milliseconds()
		if err != nil {
			r.Checks[index].Status = "failed"
			r.Checks[index].Detail = err.Error()
			r.Status = "failed"
			for j := index + 1; j < len(r.Checks); j++ {
				r.Checks[j].Status = "blocked"
			}
			emit()
			return false
		}
		r.Checks[index].Status = "passed"
		r.Checks[index].Detail = detail
		emit()
		return true
	}
	if !step(0, func() (string, error) {
		for name, path := range map[string]string{"cwebp": tools.CWebP, "restic": tools.Restic} {
			if path == "" {
				return "", fmt.Errorf("缺少 %s；请运行 brew install webp restic", name)
			}
			arg := "-version"
			if name == "restic" {
				arg = "version"
			}
			out, err := exec.CommandContext(ctx, path, arg).Output()
			if err != nil {
				return "", fmt.Errorf("%s 无法运行", name)
			}
			r.Versions[name] = strings.TrimSpace(string(out))
		}
		r.Versions["go"] = runtime.Version()
		r.Versions["platform"] = runtime.GOOS + "/" + runtime.GOARCH
		return "编码器与备份工具可用；仅使用合成测试数据。", nil
	}) {
		return finish()
	}
	root, err := os.MkdirTemp("", "srics-m0-")
	if err != nil {
		r.Status = "failed"
		r.Checks[1].Status = "failed"
		r.Checks[1].Detail = "无法创建隔离验证目录"
		return finish()
	}
	defer os.RemoveAll(root)
	converter := media.Converter{CWebP: tools.CWebP}
	var converted media.Result
	fixture := image.NewNRGBA(image.Rect(0, 0, 24, 16))
	for y := 0; y < 16; y++ {
		for x := 0; x < 24; x++ {
			fixture.SetNRGBA(x, y, color.NRGBA{R: byte(x * 9), G: byte(y * 13), B: 173, A: byte((x + y) % 4 * 85)})
		}
	}
	var pngBytes bytes.Buffer
	png.Encode(&pngBytes, fixture)
	if !step(1, func() (string, error) {
		var err error
		converted, err = converter.Convert(ctx, pngBytes.Bytes())
		if err != nil {
			return "", err
		}
		var jpg bytes.Buffer
		if err = jpeg.Encode(&jpg, fixture, &jpeg.Options{Quality: 88}); err != nil {
			return "", err
		}
		// A valid little-endian TIFF IFD carrying Orientation=6. Pixels are
		// unrotated; EXIF is copied so a reader can apply orientation once.
		exif := []byte{'I', 'I', 42, 0, 8, 0, 0, 0, 1, 0, 0x12, 1, 3, 0, 1, 0, 0, 0, 6, 0, 0, 0, 0, 0, 0, 0}
		icc := make([]byte, 128)
		binary.BigEndian.PutUint32(icc, 128)
		copy(icc[36:], "acsp")
		data := jpg.Bytes()
		data = jpegSegment(data, 0xe1, append([]byte("Exif\x00\x00"), exif...))
		data = jpegSegment(data, 0xe2, append([]byte("ICC_PROFILE\x00\x01\x01"), icc...))
		res, err := converter.Convert(ctx, data)
		if err != nil {
			return "", err
		}
		meta, err := media.Metadata(res.Data)
		if err != nil {
			return "", err
		}
		if !bytes.Equal(meta["EXIF"], exif) || !bytes.Equal(meta["ICCP"], icc) {
			return "", errors.New("EXIF / ICC 字节不一致")
		}
		return "PNG（含完全透明像素）与 JPEG 转换后逐像素一致；EXIF 方向及合成 ICC 字节保留。", nil
	}) {
		return finish()
	}
	if !step(2, func() (string, error) {
		again, err := converter.Convert(ctx, converted.Data)
		if err != nil {
			return "", err
		}
		if !again.Preserved || !bytes.Equal(again.Data, converted.Data) {
			return "", errors.New("已有 WebP 被改写")
		}
		var high bytes.Buffer
		png.Encode(&high, image.NewNRGBA64(image.Rect(0, 0, 2, 2)))
		animated := pngChunk(pngBytes.Bytes(), "acTL", []byte{0, 0, 0, 2, 0, 0, 0, 0})
		for name, data := range map[string][]byte{"坏图": []byte("not an image"), "16 位 PNG": high.Bytes(), "动画 PNG": animated} {
			if _, err = converter.Convert(ctx, data); err == nil {
				return "", fmt.Errorf("未拒绝%s", name)
			}
		}
		names := []string{"10.webp", "00002.webp", "1.webp"}
		media.NaturalSort(names)
		if strings.Join(names, ",") != "1.webp,00002.webp,10.webp" {
			return "", errors.New("页序错误")
		}
		return "已有 WebP 字节不变；坏图、16 位及动画 PNG 被拒绝；数字页序正确。", nil
	}) {
		return finish()
	}
	var key *age.X25519Identity
	var wrapped, ciphertext []byte
	secretName := "synthetic-private-name-" + randomSecret()
	secretContent := []byte("synthetic-private-content-" + randomSecret())
	vaultPass := randomSecret()
	backupPass := randomSecret()
	if !step(3, func() (string, error) {
		var err error
		key, wrapped, err = vault.Create(vaultPass)
		if err != nil {
			return "", err
		}
		var out bytes.Buffer
		if err = vault.Encrypt(&out, bytes.NewReader(secretContent), key.Recipient()); err != nil {
			return "", err
		}
		ciphertext = out.Bytes()
		plain, err := vault.Decrypt(ciphertext, key, 1<<20)
		if err != nil || !bytes.Equal(plain, secretContent) {
			return "", errors.New("加密往返不一致")
		}
		clear(plain)
		return "标准 age 格式可解密取回；私钥仅以独立口令保护后的密文保存。", nil
	}) {
		return finish()
	}
	if !step(4, func() (string, error) {
		if _, err := vault.Unlock(wrapped, randomSecret()); err == nil {
			return "", errors.New("错误口令被接受")
		}
		changed := bytes.Clone(ciphertext)
		changed[len(changed)-1] ^= 1
		for _, data := range [][]byte{changed, ciphertext[:len(ciphertext)-1]} {
			if _, err := vault.Decrypt(data, key, 1<<20); err == nil {
				return "", errors.New("篡改或截断被接受")
			}
		}
		if _, err := vault.Decrypt(ciphertext, nil, 1<<20); !errors.Is(err, vault.ErrLocked) {
			return "", errors.New("未解锁读取未被拒绝")
		}
		return "错误口令、末尾篡改、密文截断和未解锁读取全部拒绝。", nil
	}) {
		return finish()
	}
	live := filepath.Join(root, "live")
	stage := filepath.Join(root, "snapshot")
	var db *store.Store
	var ordinaryID, privateID string
	var closed bool
	defer func() {
		if db != nil && !closed {
			db.Close()
		}
	}()
	if !step(5, func() (string, error) {
		var err error
		db, err = store.Create(live, wrapped)
		if err != nil {
			return "", err
		}
		ordinaryID, err = db.Put(ctx, "00001.webp", bytes.NewReader(converted.Data), nil, false)
		if err != nil {
			return "", err
		}
		privateID, err = db.Put(ctx, secretName, bytes.NewReader(secretContent), key, true)
		if err != nil {
			return "", err
		}
		if _, _, err = db.ReadFixture(ctx, privateID, nil); !errors.Is(err, vault.ErrLocked) {
			return "", errors.New("私密读取未拒绝")
		}
		_, data, err := db.ReadFixture(ctx, privateID, key)
		if err != nil || !bytes.Equal(data, secretContent) {
			return "", errors.New("私密文件存取不一致")
		}
		return "普通与私密对象分区保存，元数据加密；文件持久化后才提交 SQLite 索引。", nil
	}) {
		return finish()
	}
	if !step(6, func() (string, error) {
		err := scanPlaintext(live, [][]byte{[]byte(secretName), secretContent, []byte(key.String()), []byte(vaultPass), []byte(backupPass)})
		if err != nil {
			return "", err
		}
		return "数据库、WAL、对象及临时文件中未发现合成私密名称、内容、私钥或口令。", nil
	}) {
		return finish()
	}
	if !step(7, func() (string, error) {
		if err := db.Snapshot(ctx, stage); err != nil {
			return "", err
		}
		if _, err := db.Put(ctx, "after-snapshot", bytes.NewBufferString("not in recovery point"), nil, false); err != nil {
			return "", err
		}
		snap, err := store.Open(stage)
		if err != nil {
			return "", err
		}
		defer snap.Close()
		objects, err := snap.List(ctx)
		if err != nil || len(objects) != 2 {
			return "", errors.New("快照混入之后的写入")
		}
		return "通过 SQLite Backup API 取得快照；之后的新写入不进入该恢复点。", nil
	}) {
		return finish()
	}
	client := backup.Client{Binary: tools.Restic, Repository: filepath.Join(root, "repository"), Password: backupPass}
	var snapshot string
	if !step(8, func() (string, error) {
		key = nil // Restic receives only the staged files and its own password.
		if err := client.Init(ctx); err != nil {
			return "", err
		}
		var err error
		snapshot, err = client.Backup(ctx, stage)
		if err != nil {
			return "", err
		}
		return "未提供保险库口令，restic 完成密文与受保护密钥文件的加密备份。", nil
	}) {
		return finish()
	}
	if !step(9, func() (string, error) {
		if err := client.Check(ctx); err != nil {
			return "", err
		}
		return "restic check --read-data 已读取并校验全部测试备份数据。", nil
	}) {
		return finish()
	}
	if !step(10, func() (string, error) {
		if err := db.Close(); err != nil {
			return "", err
		}
		closed = true
		// These directories were created by this run and contain synthetic data
		// only. Removing both proves recovery cannot accidentally read originals.
		if err := os.RemoveAll(live); err != nil {
			return "", err
		}
		if err := os.RemoveAll(stage); err != nil {
			return "", err
		}
		target := filepath.Join(root, "restored")
		if err := client.Restore(ctx, snapshot, target); err != nil {
			return "", err
		}
		restored, err := store.Open(target)
		if err != nil {
			return "", err
		}
		defer restored.Close()
		recoveredWrapped, err := restored.WrappedIdentity()
		if err != nil {
			return "", err
		}
		recoveredKey, err := vault.Unlock(recoveredWrapped, vaultPass)
		if err != nil {
			return "", err
		}
		meta, data, err := restored.ReadFixture(ctx, privateID, recoveredKey)
		if err != nil || meta.Name != secretName || !bytes.Equal(data, secretContent) {
			return "", errors.New("恢复的私密内容不一致")
		}
		_, ordinary, err := restored.ReadFixture(ctx, ordinaryID, nil)
		if err != nil || !bytes.Equal(ordinary, converted.Data) {
			return "", errors.New("恢复的原件不一致")
		}
		objects, err := restored.List(ctx)
		if err != nil || len(objects) != 2 {
			return "", errors.New("恢复的索引范围不一致")
		}
		return "已移除工作数据和快照源目录，仅凭 restic 仓库及独立口令恢复索引、原件、私密名称与内容。", nil
	}) {
		return finish()
	}
	r.Status = "passed"
	return finish()
}

func randomSecret() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}
func scanPlaintext(root string, secrets [][]byte) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, secret := range secrets {
			if bytes.Contains(data, secret) {
				return errors.New("发现私密测试明文残留")
			}
		}
		return nil
	})
}
func jpegSegment(jpg []byte, marker byte, p []byte) []byte {
	var out bytes.Buffer
	out.Write(jpg[:2])
	out.Write([]byte{0xff, marker, byte((len(p) + 2) >> 8), byte(len(p) + 2)})
	out.Write(p)
	out.Write(jpg[2:])
	return out.Bytes()
}
func pngChunk(pngData []byte, kind string, p []byte) []byte {
	var out bytes.Buffer
	out.Write(pngData[:33])
	binary.Write(&out, binary.BigEndian, uint32(len(p)))
	out.WriteString(kind)
	out.Write(p)
	binary.Write(&out, binary.BigEndian, crc32.ChecksumIEEE(append([]byte(kind), p...)))
	out.Write(pngData[33:])
	return out.Bytes()
}
