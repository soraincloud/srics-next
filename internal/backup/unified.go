package backup

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/soraincloud/srics-next/internal/recoverykey"
)

// Cache paths are internal implementation details, never a second user backup.
type UnifiedConfig struct {
	Repository      string    `json:"repository"`
	PasswordFile    string    `json:"passwordFile"`
	Directory       string    `json:"directory"`
	Cloud           *S3Config `json:"cloud,omitempty"`
	CredentialsFile string    `json:"credentialsFile,omitempty"`
}

func canonical(path string) (string, error) {
	p, err := filepath.EvalSymlinks(path)
	if err == nil {
		return p, nil
	}
	if !os.IsNotExist(err) {
		return "", err
	}
	parent := filepath.Dir(path)
	if parent == path {
		return "", err
	}
	p, err = canonical(parent)
	return filepath.Join(p, filepath.Base(path)), err
}
func Disjoint(a, b string) error {
	if !filepath.IsAbs(a) || !filepath.IsAbs(b) {
		return errors.New("路径必须是绝对路径")
	}
	x, err := canonical(a)
	if err != nil {
		return err
	}
	y, err := canonical(b)
	if err != nil {
		return err
	}
	for _, pair := range [][2]string{{x, y}, {y, x}} {
		r, err := filepath.Rel(pair[0], pair[1])
		if err != nil || r == "." || (!strings.HasPrefix(r, ".."+string(os.PathSeparator)) && r != "..") {
			return errors.New("资料、备份与内部钥匙的存放位置不能相互包含")
		}
	}
	return nil
}
func (c UnifiedConfig) Validate(data string) error {
	paths := []string{data, c.Repository, c.PasswordFile}
	if c.Directory != "" {
		paths = append(paths, c.Directory)
	}
	if c.Cloud != nil {
		if err := c.Cloud.Validate(); err != nil {
			return err
		}
		paths = append(paths, c.CredentialsFile)
	}
	if c.Directory == "" && c.Cloud == nil {
		return errors.New("请选择备份保存位置")
	}
	for i, a := range paths {
		if a == "" || filepath.Clean(a) == "/" {
			return errors.New("备份配置路径无效")
		}
		for _, b := range paths[:i] {
			if err := Disjoint(a, b); err != nil {
				return err
			}
		}
	}
	return nil
}

type Unified struct {
	Config             UnifiedConfig
	Binary             string
	ProtectedDirectory string
}
type PackageResult struct {
	File         string   `json:"file"`
	Snapshot     string   `json:"snapshot"`
	Bytes        int64    `json:"bytes"`
	Destinations []string `json:"destinations"`
	Warning      string   `json:"warning,omitempty"`
}

func readPrivate(path string, limit int64) ([]byte, error) {
	i, err := os.Lstat(path)
	if err != nil || !i.Mode().IsRegular() || i.Mode().Perm()&0077 != 0 {
		return nil, errors.New("本机备份钥匙不可读取，请检查配置目录")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !os.SameFile(i, fi) {
		return nil, errors.New("本机备份钥匙已变化")
	}
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(b)) > limit {
		clear(b)
		return nil, errors.New("本机备份钥匙无效")
	}
	return b, nil
}
func (u Unified) Client() (Client, error) {
	b, err := readPrivate(u.Config.PasswordFile, 4096)
	if err != nil {
		return Client{}, err
	}
	defer clear(b)
	password := strings.TrimRight(string(b), "\r\n")
	if len(password) < 12 {
		return Client{}, errors.New("本机备份钥匙无效")
	}
	return Client{Binary: u.Binary, Repository: u.Config.Repository, Password: password}, nil
}
func (u Unified) CloudClient() (Client, error) {
	var c Client
	c.S3 = u.Config.Cloud
	b, err := readPrivate(u.Config.CredentialsFile, 32768)
	if err != nil {
		return c, err
	}
	defer clear(b)
	if json.Unmarshal(b, &c.Credentials) != nil {
		return c, errors.New("云端访问配置无效")
	}
	return c, c.Credentials.Validate()
}
func (u Unified) Destinations() []string {
	d := []string{}
	if u.Config.Directory != "" {
		d = append(d, u.Config.Directory)
	}
	if u.Config.Cloud != nil {
		d = append(d, "s3://"+u.Config.Cloud.Bucket+"/"+u.Config.Cloud.Prefix)
	}
	return d
}
func (u Unified) Fingerprint() string {
	b, _ := json.Marshal(u.Config)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func (u Unified) Run(ctx context.Context, l UnifiedLibrary, file string) (PackageResult, error) {
	var out PackageResult
	if err := u.Config.Validate(l.RootPath()); err != nil {
		return out, err
	}
	root, err := l.RecoveryRecord()
	if err != nil {
		return out, err
	}
	if root == nil || root.State != "verified" {
		return out, errors.New("请先在 App 中保存并验证资料库恢复 JSON")
	}
	// Missing cache is an error. Only the explicit setup action initializes it.
	client, err := u.Client()
	if err != nil {
		return out, err
	}
	repositoryID, err := client.RepositoryID(ctx)
	if err != nil {
		return out, errors.New("内部备份缓存不可读取，请通过备份设置重新配置；不会创建替代仓库")
	}
	manual := file != ""
	if u.ProtectedDirectory != "" {
		output := u.Config.Directory
		if manual {
			output = file
		}
		if output != "" {
			if err = Disjoint(output, u.ProtectedDirectory); err != nil {
				return out, errors.New("请将备份保存在配置目录之外")
			}
		}
	}
	if root.ExportPath != "" {
		if _, e := os.Stat(root.ExportPath); e == nil {
			output := u.Config.Directory
			if manual {
				output = filepath.Dir(file)
			}
			if output != "" {
				if e = Disjoint(root.ExportPath, output); e != nil {
					return out, errors.New("此备份位置包含恢复 JSON，请把 JSON 移到独立位置，再在恢复钥匙中重新选择验证")
				}
			}
		}
	}
	if file == "" {
		dir := u.Config.Directory
		if dir == "" {
			dir = filepath.Join(filepath.Dir(u.Config.PasswordFile), "pending")
			if err = os.MkdirAll(dir, 0700); err != nil {
				return out, err
			}
		}
		// A disconnected drive must fail, never recreate a missing destination.
		info, e := os.Stat(dir)
		if e != nil || !info.IsDir() {
			return out, errors.New("备份位置不可用，请检查磁盘或同步目录")
		}
		file = filepath.Join(dir, "SRICS-"+time.Now().Format("20060102-150405")+"-"+packageID()[:8]+".sricsbackup")
	}
	if filepath.Ext(file) != ".sricsbackup" {
		return out, errors.New("备份文件需以 .sricsbackup 结尾")
	}
	for _, p := range []string{l.RootPath(), u.Config.Repository, u.Config.PasswordFile} {
		if err = Disjoint(file, p); err != nil {
			return out, err
		}
	}
	if _, e := os.Lstat(file); !os.IsNotExist(e) {
		return out, errors.New("备份文件已存在或不可写，请选择新文件名")
	}
	stage := filepath.Join(l.RootPath(), "staging", "unified-"+packageID())
	defer os.RemoveAll(stage)
	if err = l.Snapshot(ctx, stage); err != nil {
		return out, err
	}
	if err = l.VerifySnapshot(ctx, stage); err != nil {
		return out, err
	}
	snapshot, err := client.BackupLibrary(ctx, stage)
	if err != nil {
		return out, err
	}
	if err = client.Check(ctx); err != nil {
		return out, errors.New("备份缓存完整读取校验失败，未发布新备份")
	}
	envelope := recoverykey.Envelope{LibraryID: root.LibraryID, RepositoryID: repositoryID, Snapshot: snapshot, Password: client.Password}
	plain, err := json.Marshal(envelope)
	if err != nil {
		return out, err
	}
	defer clear(plain)
	sealed, err := recoverykey.Seal(root.Recipient, plain)
	if err != nil {
		return out, err
	}
	m := PackageManifest{Version: 2, LibraryID: root.LibraryID, RepositoryID: repositoryID, Snapshot: snapshot, RecoveryKeyID: root.ID, RecoveryEnvelope: sealed}
	if err = ExportPackage(ctx, client.Repository, file, m); err != nil {
		return out, err
	}
	info, err := os.Stat(file)
	if err != nil {
		return out, err
	}
	out = PackageResult{File: file, Snapshot: snapshot, Bytes: info.Size(), Destinations: []string{file}}
	if !manual && u.Config.Cloud != nil {
		cloud, e := u.CloudClient()
		if e != nil {
			return out, e
		}
		object, e := cloud.UploadPackage(ctx, file)
		if e != nil {
			return out, fmt.Errorf("云端备份未通过上传与读回校验，本地文件保留在 %s", file)
		}
		out.Destinations = append(out.Destinations, object)
		if u.Config.Directory == "" && filepath.Dir(file) == filepath.Join(filepath.Dir(u.Config.PasswordFile), "pending") {
			if err = os.Remove(file); err != nil {
				return out, err
			}
			out.File = object
			out.Destinations = []string{object}
		}
	}
	// All published packages are independent encrypted copies. Compact only the
	// internal regenerable cache after every selected destination is verified.
	policy := Retention{Enabled: true, Daily: 1, Monthly: 0}
	cachePlan, e := client.PlanRetention(ctx, policy)
	if e == nil && len(cachePlan.Remove) > 0 {
		e = client.ApplyRetention(ctx, policy, cachePlan.Token)
	}
	if e != nil {
		out.Warning = "备份已保存，但内部缓存整理未完成；下次备份会重试，请检查本机可用空间"
	}
	return out, nil
}
func (c Client) UploadPackage(ctx context.Context, file string) (string, error) {
	if filepath.Ext(file) != ".sricsbackup" {
		return "", errors.New("无效备份文件")
	}
	client, transport, err := c.s3Client()
	if err != nil {
		return "", err
	}
	defer transport.CloseIdleConnections()
	f, err := os.Open(file)
	if err != nil {
		return "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return "", errors.New("备份文件不可读取")
	}
	h := sha256.New()
	if _, err = io.Copy(h, contextReader{ctx, f}); err != nil {
		return "", err
	}
	expected := h.Sum(nil)
	if _, err = f.Seek(0, 0); err != nil {
		return "", err
	}
	name := strings.Trim(c.S3.Prefix, "/") + "/" + filepath.Base(file)
	// Unique names plus conditional writes protect all existing remote versions.
	opts := minio.PutObjectOptions{ContentType: "application/octet-stream", PartSize: 16 << 20, NumThreads: 2}
	opts.SetMatchETagExcept("*")
	if _, err = client.PutObject(ctx, c.S3.Bucket, name, contextReader{ctx, f}, info.Size(), opts); err != nil {
		return "", errors.New("云端上传失败")
	}
	remote, err := client.GetObject(ctx, c.S3.Bucket, name, minio.GetObjectOptions{})
	if err != nil {
		return "", err
	}
	defer remote.Close()
	h.Reset()
	n, err := io.Copy(h, contextReader{ctx, io.LimitReader(remote, info.Size()+1)})
	if err != nil || n != info.Size() || !slices.Equal(h.Sum(nil), expected) {
		return "", errors.New("云端读回校验失败")
	}
	return "s3://" + c.S3.Bucket + "/" + name, nil
}
func (u Unified) History(ctx context.Context) ([]Snapshot, error) {
	entries := map[string]Snapshot{}
	if u.Config.Directory != "" {
		files, err := os.ReadDir(u.Config.Directory)
		if err != nil {
			return nil, errors.New("备份位置不可读取")
		}
		for _, f := range files {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if len(entries) > 10000 {
				return nil, errors.New("历史超过 10000 份，请按年份整理备份目录")
			}
			if f.Type().IsRegular() && strings.HasPrefix(f.Name(), "SRICS-") && filepath.Ext(f.Name()) == ".sricsbackup" {
				m, e := InspectPackage(filepath.Join(u.Config.Directory, f.Name()))
				if e != nil {
					return nil, errors.New("备份目录中有损坏的备份清单")
				}
				i, e := f.Info()
				if e != nil {
					return nil, e
				}
				entries[f.Name()] = Snapshot{ID: f.Name(), Time: m.CreatedAt, Bytes: packageBytes(i.Size()), RecoveryKeys: []string{m.RecoveryKeyID}}
			}
		}
	}
	if u.Config.Cloud != nil {
		c, err := u.CloudClient()
		if err != nil {
			return nil, err
		}
		client, t, err := c.s3Client()
		if err != nil {
			return nil, err
		}
		defer t.CloseIdleConnections()
		for object := range client.ListObjects(ctx, c.S3.Bucket, minio.ListObjectsOptions{Prefix: strings.Trim(c.S3.Prefix, "/") + "/", Recursive: false}) {
			if object.Err != nil {
				return nil, errors.New("云端历史不可读取")
			}
			name := filepath.Base(object.Key)
			if strings.HasPrefix(name, "SRICS-") && filepath.Ext(name) == ".sricsbackup" {
				if _, ok := entries[name]; !ok {
					entries[name] = Snapshot{ID: name, Time: object.LastModified, Bytes: packageBytes(object.Size)}
				}
			}
			if len(entries) > 10000 {
				return nil, errors.New("历史超过 10000 份，请按年份整理备份目录")
			}
		}
	}
	result := []Snapshot{}
	for _, e := range entries {
		result = append(result, e)
	}
	slices.SortFunc(result, func(a, b Snapshot) int { return b.Time.Compare(a.Time) })
	return result, nil
}

func packageBytes(n int64) *uint64 { v := uint64(n); return &v }

// A package prefix must not be a legacy restic repository or unrelated folder.
func (c Client) ProbePackages(ctx context.Context) error {
	client, t, err := c.s3Client()
	if err != nil {
		return err
	}
	defer t.CloseIdleConnections()
	exists, err := client.BucketExists(ctx, c.S3.Bucket)
	if err != nil || !exists {
		return errors.New("云端存储桶不可访问，请检查地址、账号与权限")
	}
	count := 0
	for entry := range client.ListObjects(ctx, c.S3.Bucket, minio.ListObjectsOptions{Prefix: strings.Trim(c.S3.Prefix, "/") + "/", Recursive: true}) {
		if entry.Err != nil {
			return errors.New("无法读取云端保存位置")
		}
		if filepath.Ext(entry.Key) != ".sricsbackup" || !strings.HasPrefix(filepath.Base(entry.Key), "SRICS-") {
			return errors.New("此云端位置包含旧版仓库或其他文件，请使用新的专用前缀，例如 srics/backups")
		}
		count++
		if count > 10000 {
			return errors.New("此位置备份过多，请使用新的年份前缀")
		}
	}
	return nil
}

// Keep repository primitives independent from library implementations, including
// integration tests that restore a library through the backup package.
type UnifiedLibrary interface {
	RootPath() string
	RecoveryRecord() (*recoverykey.Record, error)
	Snapshot(context.Context, string) error
	VerifySnapshot(context.Context, string) error
}

func packageID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
