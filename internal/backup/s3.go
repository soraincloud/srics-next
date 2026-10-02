package backup

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

type S3Config struct {
	Endpoint string `json:"endpoint"`
	Region   string `json:"region"`
	Bucket   string `json:"bucket"`
	Prefix   string `json:"prefix"`
	Lookup   string `json:"lookup"`
	CAFile   string `json:"caFile"`
}

type Credentials struct {
	AccessKeyID     string `json:"accessKeyId"`
	SecretAccessKey string `json:"secretAccessKey"`
	SessionToken    string `json:"sessionToken,omitempty"`
}

var bucketName = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
var regionName = regexp.MustCompile(`^[a-z0-9-]{1,64}$`)
var prefixName = regexp.MustCompile(`^[A-Za-z0-9_./-]{1,200}$`)

func (s S3Config) Validate() error {
	u, err := url.Parse(s.Endpoint)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" || (u.Path != "" && u.Path != "/") || u.RawPath != "" {
		return errors.New("云端 Endpoint 需为 HTTPS 服务地址，不含桶名、路径、账号或查询参数")
	}
	if !regionName.MatchString(s.Region) {
		return errors.New("请填写有效的云端区域")
	}
	if !bucketName.MatchString(s.Bucket) || strings.Contains(s.Bucket, "..") || net.ParseIP(s.Bucket) != nil {
		return errors.New("请填写有效的存储桶名称")
	}
	if !prefixName.MatchString(s.Prefix) || path.Clean(s.Prefix) != s.Prefix || strings.HasPrefix(s.Prefix, "/") || s.Prefix == "." || strings.HasPrefix(s.Prefix, "../") || s.Prefix == ".." {
		return errors.New("请设置专用的云端备份前缀，例如 srics/main")
	}
	if s.Lookup != "" && s.Lookup != "auto" && s.Lookup != "path" && s.Lookup != "dns" {
		return errors.New("无效的 S3 寻址方式")
	}
	return nil
}

func (s S3Config) Repository() string {
	return "s3:" + strings.TrimRight(s.Endpoint, "/") + "/" + s.Bucket + "/" + s.Prefix
}
func (s S3Config) lookup() string {
	if s.Lookup == "" {
		return "auto"
	}
	return s.Lookup
}
func (c Credentials) Validate() error {
	if c.AccessKeyID == "" || c.SecretAccessKey == "" || len(c.AccessKeyID) > 256 || len(c.SecretAccessKey) > 4096 || len(c.SessionToken) > 16384 || strings.ContainsAny(c.AccessKeyID+c.SecretAccessKey+c.SessionToken, "\x00\r\n") {
		return errors.New("请填写有效的 Access Key ID 与 Secret Access Key")
	}
	return nil
}

func (c Client) s3Client() (*minio.Client, *http.Transport, error) {
	if c.S3 == nil {
		return nil, nil, errors.New("未配置 S3 存储")
	}
	if err := c.S3.Validate(); err != nil {
		return nil, nil, err
	}
	if err := c.Credentials.Validate(); err != nil {
		return nil, nil, err
	}
	u, _ := url.Parse(c.S3.Endpoint)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	transport.ResponseHeaderTimeout = 15 * time.Second
	if c.S3.CAFile != "" {
		data, err := os.ReadFile(c.S3.CAFile)
		if err != nil {
			return nil, nil, errors.New("无法读取云端 CA 文件")
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(data) {
			return nil, nil, errors.New("云端 CA 文件不是有效 PEM 证书")
		}
		transport.TLSClientConfig.RootCAs = pool
	}
	lookup := minio.BucketLookupAuto
	if c.S3.lookup() == "dns" {
		lookup = minio.BucketLookupDNS
	}
	if c.S3.lookup() == "path" {
		lookup = minio.BucketLookupPath
	}
	client, err := minio.New(u.Host, &minio.Options{Creds: credentials.NewStaticV4(c.Credentials.AccessKeyID, c.Credentials.SecretAccessKey, c.Credentials.SessionToken), Secure: true, Region: c.S3.Region, BucketLookup: lookup, Transport: transport})
	if err != nil {
		transport.CloseIdleConnections()
		return nil, nil, errors.New("无法建立云端连接，请检查 Endpoint")
	}
	return client, transport, nil
}

// ProbeCloud is read-only. It never creates buckets or changes repository data.
// Result is "ready" (existing, authenticated restic repository) or "empty".
func (c Client) ProbeCloud(ctx context.Context) (string, error) {
	client, transport, err := c.s3Client()
	if err != nil {
		return "", err
	}
	defer transport.CloseIdleConnections()
	found, err := client.BucketExists(ctx, c.S3.Bucket)
	if err != nil {
		return "", errors.New("无法访问存储桶，请检查连接、区域与凭据权限")
	}
	if !found {
		return "", errors.New("存储桶不存在，请先在云服务商创建；程序不会自动建桶")
	}
	_, err = client.StatObject(ctx, c.S3.Bucket, c.S3.Prefix+"/config", minio.StatObjectOptions{})
	if err == nil {
		if _, err = c.run(ctx, "", "cat", "config"); err != nil {
			return "", errors.New("已有云端仓库无法解锁，请检查备份口令与读取权限")
		}
		return "ready", nil
	}
	code := minio.ToErrorResponse(err).Code
	if code != "NoSuchKey" && code != "NotFound" {
		return "", errors.New("无法读取云端仓库，请检查连接与权限")
	}
	listCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	for object := range client.ListObjects(listCtx, c.S3.Bucket, minio.ListObjectsOptions{Prefix: c.S3.Prefix + "/", Recursive: true, MaxKeys: 1}) {
		if object.Err != nil {
			return "", errors.New("无法检查备份前缀，请检查列举权限")
		}
		return "", errors.New("云端前缀非空且不是可用备份仓库，请选择新的专用前缀")
	}
	if ctx.Err() != nil {
		return "", errors.New("云端连接检查已中断")
	}
	return "empty", nil
}

func (c Client) PrepareCloud(ctx context.Context, initialized bool) error {
	state, err := c.ProbeCloud(ctx)
	if err != nil {
		return err
	}
	if state == "ready" {
		return nil
	}
	if initialized {
		return errors.New("已有云端仓库缺失，未重新初始化")
	}
	return c.Init(ctx)
}
