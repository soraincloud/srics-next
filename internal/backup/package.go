package backup

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/soraincloud/srics-next/internal/atomicfile"
	"golang.org/x/sys/unix"
)

// A portable package contains an encrypted restic repository, never the source
// library or a password. Store avoids recompressing ciphertext and supports ZIP64.
const packageManifestName = "srics-package.json"
const maxPackageBytes uint64 = 1 << 40
const maxPackageFiles = 200000
const maxManifestBytes = 32 << 20

type PackageFile struct {
	Name   string `json:"name"`
	Size   uint64 `json:"size"`
	SHA256 string `json:"sha256"`
}
type PackageManifest struct {
	Format        string        `json:"format"`
	Version       int           `json:"version"`
	CreatedAt     time.Time     `json:"createdAt"`
	RepositoryID  string        `json:"repositoryID"`
	Snapshot      string        `json:"snapshot"`
	RecoveryKeyID string        `json:"recoveryKeyID"`
	Files         []PackageFile `json:"files"`
}

var resticPackageDirectory = regexp.MustCompile(`^data/[a-f0-9]{2}$`)
var resticPackagePath = regexp.MustCompile(`^(config|(?:keys|index|snapshots)/[a-f0-9]{64}|data/[a-f0-9]{2}/[a-f0-9]{64})$`)

func validPackageName(name string) bool {
	if !resticPackagePath.MatchString(name) {
		return false
	}
	if strings.HasPrefix(name, "data/") {
		parts := strings.Split(name, "/")
		return parts[1] == parts[2][:2]
	}
	return true
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

func packageInventory(repo string) ([]string, error) {
	info, err := os.Lstat(repo)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("备份仓库必须是实际目录")
	}
	var names []string
	err = filepath.WalkDir(repo, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == repo {
			return nil
		}
		name, e := filepath.Rel(repo, path)
		if e != nil {
			return e
		}
		name = filepath.ToSlash(name)
		if name == "locks" {
			if !d.IsDir() {
				return errors.New("备份锁目录无效")
			}
			entries, e := os.ReadDir(path)
			if e != nil {
				return e
			}
			if len(entries) != 0 {
				return errors.New("备份仓库正在使用，请等待其他备份任务结束")
			}
			return filepath.SkipDir
		}
		if d.Type()&os.ModeSymlink != 0 {
			return errors.New("备份仓库包含符号链接，未导出")
		}
		if d.IsDir() {
			if name == "data" || name == "index" || name == "keys" || name == "snapshots" || resticPackageDirectory.MatchString(name) {
				return nil
			}
			return fmt.Errorf("备份仓库含未知目录：%s", name)
		}
		if d.Type().IsRegular() && (d.Name() == ".DS_Store" || strings.HasPrefix(d.Name(), "._")) {
			return nil
		}
		info, e := d.Info()
		if e != nil {
			return e
		}
		if !info.Mode().IsRegular() || !validPackageName(name) {
			return fmt.Errorf("备份仓库含未知文件：%s", name)
		}
		names = append(names, name)
		if len(names) > maxPackageFiles {
			return errors.New("备份包文件数量超出限制")
		}
		return nil
	})
	slices.Sort(names)
	if err == nil && !slices.Contains(names, "config") {
		err = errors.New("备份仓库缺少配置")
	}
	return names, err
}

// The final name is published only after ZIP CRC and manifest hashes read back.
// Atomic WriteNew never replaces an existing backup. All I/O has bounded memory.
func ExportPackage(ctx context.Context, repository, file string, manifest PackageManifest) error {
	names, err := packageInventory(repository)
	if err != nil {
		return err
	}
	manifest.Format, manifest.Version, manifest.CreatedAt = "srics-encrypted-backup", 1, time.Now().UTC()
	manifest.Files = nil
	var expected uint64
	for _, name := range names {
		info, err := os.Lstat(filepath.Join(repository, filepath.FromSlash(name)))
		if err != nil || !info.Mode().IsRegular() {
			return errors.New("备份文件已变化，未导出")
		}
		if uint64(info.Size()) > maxPackageBytes-expected {
			return errors.New("备份包超过 1 TiB 限制")
		}
		expected += uint64(info.Size())
	}
	if err := packageSpace(filepath.Dir(file), expected+(128<<20)); err != nil {
		return err
	}
	return atomicfile.WriteNew(file, func(w io.Writer) error {
		f, ok := w.(*os.File)
		if !ok {
			return errors.New("备份输出不是文件")
		}
		zw := zip.NewWriter(f)
		var total uint64
		for _, name := range names {
			if err := ctx.Err(); err != nil {
				return err
			}
			src, err := os.Open(filepath.Join(repository, filepath.FromSlash(name)))
			if err != nil {
				return err
			}
			info, err := src.Stat()
			if err != nil || !info.Mode().IsRegular() {
				src.Close()
				return errors.New("备份文件已变化，未导出")
			}
			// Lstat also rejects a source swapped to a symlink before Open.
			link, err := os.Lstat(src.Name())
			if err != nil || !os.SameFile(info, link) || !link.Mode().IsRegular() {
				src.Close()
				return errors.New("备份文件已变化，未导出")
			}
			size := uint64(info.Size())
			if size > maxPackageBytes-total {
				src.Close()
				return errors.New("备份包超过 1 TiB 限制")
			}
			total += size
			header := &zip.FileHeader{Name: "repository/" + name, Method: zip.Store}
			header.SetMode(0600)
			out, err := zw.CreateHeader(header)
			if err != nil {
				src.Close()
				return err
			}
			h := sha256.New()
			n, err := io.Copy(io.MultiWriter(out, h), contextReader{ctx, src})
			after, statErr := src.Stat()
			src.Close()
			if err != nil {
				return err
			}
			if statErr != nil || n != info.Size() || after.Size() != info.Size() || !after.ModTime().Equal(info.ModTime()) {
				return errors.New("备份文件在导出期间变化，请重试")
			}
			manifest.Files = append(manifest.Files, PackageFile{Name: name, Size: size, SHA256: hex.EncodeToString(h.Sum(nil))})
		}
		after, err := packageInventory(repository)
		if err != nil {
			return err
		}
		if !slices.Equal(names, after) {
			return errors.New("备份仓库在导出期间变化，请重试")
		}
		data, err := json.Marshal(manifest)
		if err != nil {
			return err
		}
		if len(data) > maxManifestBytes {
			return errors.New("备份清单超出限制")
		}
		out, err := zw.CreateHeader(&zip.FileHeader{Name: packageManifestName, Method: zip.Store})
		if err != nil {
			return err
		}
		if _, err = out.Write(data); err != nil {
			return err
		}
		if err = zw.Close(); err != nil {
			return err
		}
		info, err := f.Stat()
		if err != nil {
			return err
		}
		zr, err := zip.NewReader(f, info.Size())
		if err != nil {
			return err
		}
		_, err = readPackage(ctx, zr, "")
		return err
	})
}

// Import into a unique new directory only. On failure remove only the directory
// created by this call. Untrusted paths, links, duplicates and extra files fail.
func ImportPackage(ctx context.Context, file, directory string) (PackageManifest, error) {
	var empty PackageManifest
	f, zr, err := openPackageFile(file)
	if err != nil {
		return empty, err
	}
	defer f.Close()
	manifest, err := packageStructure(zr)
	if err != nil {
		return empty, err
	}
	var expected uint64
	for _, entry := range manifest.Files {
		expected += entry.Size
	}
	if err = packageSpace(filepath.Dir(directory), expected+(128<<20)); err != nil {
		return empty, err
	}
	if err = os.Mkdir(directory, 0700); err != nil {
		return empty, errors.New("导入目录已存在或不可写，请选择新的存放位置")
	}
	succeeded := false
	defer func() {
		if !succeeded {
			os.RemoveAll(directory)
		}
	}()
	manifest, err = readPackage(ctx, zr, directory)
	if err != nil {
		return empty, err
	}
	succeeded = true
	return manifest, nil
}

func packageStructure(zr *zip.Reader) (PackageManifest, error) {
	var m PackageManifest
	if len(zr.File) < 2 || len(zr.File) > maxPackageFiles+1 {
		return m, errors.New("备份包文件数量无效")
	}
	seen := map[string]*zip.File{}
	for _, f := range zr.File {
		if seen[f.Name] != nil || !f.Mode().IsRegular() || f.Method != zip.Store || f.CompressedSize64 != f.UncompressedSize64 {
			return m, errors.New("备份包包含重复、压缩或非普通文件")
		}
		if f.Name != packageManifestName && (!strings.HasPrefix(f.Name, "repository/") || !validPackageName(strings.TrimPrefix(f.Name, "repository/"))) {
			return m, errors.New("备份包包含不允许的路径")
		}
		seen[f.Name] = f
	}
	mf := seen[packageManifestName]
	if mf == nil || mf.UncompressedSize64 > maxManifestBytes {
		return m, errors.New("备份包缺少有效清单")
	}
	r, err := mf.Open()
	if err != nil {
		return m, err
	}
	defer r.Close()
	d := json.NewDecoder(io.LimitReader(r, maxManifestBytes+1))
	d.DisallowUnknownFields()
	if d.Decode(&m) != nil || d.Decode(&struct{}{}) != io.EOF || m.Format != "srics-encrypted-backup" || m.Version != 1 || m.CreatedAt.IsZero() || !snapshotID.MatchString(m.RepositoryID) || !snapshotID.MatchString(m.Snapshot) || !snapshotID.MatchString(m.RecoveryKeyID) || len(m.Files) != len(zr.File)-1 {
		return m, errors.New("备份清单无效或版本不支持")
	}
	var total uint64
	listed := map[string]bool{}
	for _, entry := range m.Files {
		f := seen["repository/"+entry.Name]
		if !validPackageName(entry.Name) || listed[entry.Name] || f == nil || !snapshotID.MatchString(entry.SHA256) || entry.Size != f.UncompressedSize64 || entry.Size > maxPackageBytes-total {
			return m, errors.New("备份清单与文件不匹配")
		}
		total += entry.Size
		listed[entry.Name] = true
	}
	if !listed["config"] || !listed["snapshots/"+m.Snapshot] {
		return m, errors.New("备份包缺少配置或最新恢复点")
	}
	return m, nil
}

func readPackage(ctx context.Context, zr *zip.Reader, directory string) (PackageManifest, error) {
	m, err := packageStructure(zr)
	if err != nil {
		return m, err
	}
	files := map[string]*zip.File{}
	for _, f := range zr.File {
		files[f.Name] = f
	}
	for _, entry := range m.Files {
		if err = ctx.Err(); err != nil {
			return m, err
		}
		r, err := files["repository/"+entry.Name].Open()
		if err != nil {
			return m, err
		}
		h := sha256.New()
		copyEntry := func(w io.Writer) error {
			n, e := io.Copy(io.MultiWriter(w, h), contextReader{ctx, io.LimitReader(r, int64(entry.Size)+1)})
			if e != nil {
				return e
			}
			if uint64(n) != entry.Size || hex.EncodeToString(h.Sum(nil)) != entry.SHA256 {
				return errors.New("备份包文件校验失败，文件可能已损坏")
			}
			return nil
		}
		if directory == "" {
			err = copyEntry(io.Discard)
		} else {
			path := filepath.Join(directory, "repository", filepath.FromSlash(entry.Name))
			if err = os.MkdirAll(filepath.Dir(path), 0700); err == nil {
				err = atomicfile.WriteNew(path, copyEntry)
			}
		}
		closeErr := r.Close()
		if err != nil {
			return m, err
		}
		if closeErr != nil {
			return m, closeErr
		}
	}
	return m, nil
}

func packageSpace(parent string, required uint64) error {
	var stat unix.Statfs_t
	if err := unix.Statfs(parent, &stat); err != nil {
		return errors.New("存放位置不可用，请检查磁盘")
	}
	available := uint64(stat.Bavail) * uint64(stat.Bsize)
	if available < required {
		return fmt.Errorf("磁盘可用空间不足，需要至少 %.1f GiB；请选择空间充足的位置", float64(required)/(1<<30))
	}
	return nil
}

func openPackageFile(file string) (*os.File, *zip.Reader, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, nil, err
	}
	ok := false
	defer func() {
		if !ok {
			f.Close()
		}
	}()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() < 22 || uint64(info.Size()) > maxPackageBytes+(128<<20) {
		return nil, nil, errors.New("备份包不是有效文件或超出大小限制")
	}
	var end [22]byte
	endOffset := info.Size() - 22
	if _, err = f.ReadAt(end[:], endOffset); err != nil || binary.LittleEndian.Uint32(end[:4]) != 0x06054b50 || binary.LittleEndian.Uint16(end[20:]) != 0 || binary.LittleEndian.Uint16(end[4:]) != 0 || binary.LittleEndian.Uint16(end[6:]) != 0 {
		return nil, nil, errors.New("备份包不完整或格式无效")
	}
	count := uint64(binary.LittleEndian.Uint16(end[10:]))
	size := uint64(binary.LittleEndian.Uint32(end[12:]))
	offset := uint64(binary.LittleEndian.Uint32(end[16:]))
	if binary.LittleEndian.Uint16(end[8:]) != binary.LittleEndian.Uint16(end[10:]) {
		return nil, nil, errors.New("不支持分卷备份包")
	}
	if count == 65535 || size == 0xffffffff || offset == 0xffffffff {
		var locator [20]byte
		if endOffset < 76 {
			return nil, nil, errors.New("ZIP64 备份包不完整")
		}
		if _, err = f.ReadAt(locator[:], endOffset-20); err != nil || binary.LittleEndian.Uint32(locator[:4]) != 0x07064b50 || binary.LittleEndian.Uint32(locator[4:]) != 0 || binary.LittleEndian.Uint32(locator[16:]) != 1 {
			return nil, nil, errors.New("ZIP64 备份包无效")
		}
		pos := binary.LittleEndian.Uint64(locator[8:])
		if pos > uint64(endOffset-20)-56 {
			return nil, nil, errors.New("ZIP64 备份包位置无效")
		}
		var extended [56]byte
		if _, err = f.ReadAt(extended[:], int64(pos)); err != nil || binary.LittleEndian.Uint32(extended[:4]) != 0x06064b50 || binary.LittleEndian.Uint64(extended[4:]) < 44 || binary.LittleEndian.Uint32(extended[16:]) != 0 || binary.LittleEndian.Uint32(extended[20:]) != 0 || binary.LittleEndian.Uint64(extended[24:]) != binary.LittleEndian.Uint64(extended[32:]) {
			return nil, nil, errors.New("ZIP64 备份清单无效")
		}
		count, size, offset = binary.LittleEndian.Uint64(extended[32:]), binary.LittleEndian.Uint64(extended[40:]), binary.LittleEndian.Uint64(extended[48:])
		endOffset = int64(pos)
	}
	if count < 2 || count > maxPackageFiles+1 || size > 128<<20 || offset > uint64(endOffset) || size > uint64(endOffset)-offset {
		return nil, nil, errors.New("备份包目录数量、大小或位置无效")
	}
	if err = boundedPackageCatalog(f, offset, size, count); err != nil {
		return nil, nil, err
	}
	zr, err := zip.NewReader(f, info.Size())
	if err != nil {
		return nil, nil, errors.New("备份包不完整或格式无效")
	}
	ok = true
	return f, zr, nil
}

// Inspect just the bounded manifest so a wrong JSON is rejected before copying
// the repository. Import repeats all validation on its own open file.
func InspectPackage(file string) (PackageManifest, error) {
	f, zr, err := openPackageFile(file)
	if err != nil {
		return PackageManifest{}, err
	}
	defer f.Close()
	return packageStructure(zr)
}

// Validate actual central records before archive/zip allocates one object per
// record. A dishonest count cannot force an unbounded metadata allocation.
func boundedPackageCatalog(f *os.File, offset, size, count uint64) error {
	r := io.NewSectionReader(f, int64(offset), int64(size))
	var consumed uint64
	var header [46]byte
	for i := uint64(0); i < count; i++ {
		if _, err := io.ReadFull(r, header[:]); err != nil || binary.LittleEndian.Uint32(header[:4]) != 0x02014b50 {
			return errors.New("备份包中央目录无效")
		}
		nameSize := uint64(binary.LittleEndian.Uint16(header[28:]))
		extraSize := uint64(binary.LittleEndian.Uint16(header[30:]))
		commentSize := uint64(binary.LittleEndian.Uint16(header[32:]))
		if nameSize == 0 || nameSize > 128 || extraSize > 1024 || commentSize != 0 || binary.LittleEndian.Uint16(header[10:]) != zip.Store || binary.LittleEndian.Uint16(header[8:])&1 != 0 {
			return errors.New("备份包中央目录包含不允许的条目")
		}
		consumed += 46 + nameSize + extraSize
		if consumed > size {
			return errors.New("备份包中央目录越界")
		}
		if _, err := io.CopyN(io.Discard, r, int64(nameSize+extraSize)); err != nil {
			return errors.New("备份包中央目录不完整")
		}
	}
	if consumed != size {
		return errors.New("备份包中央目录数量不匹配")
	}
	return nil
}
