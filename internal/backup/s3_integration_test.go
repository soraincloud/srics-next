//go:build integration

package backup

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"encoding/pem"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// A bounded, in-memory S3 protocol fixture. SigV4 validation is test-only;
// production signing is provided by the SDK and by restic.
type s3Fixture struct {
	mu      sync.Mutex
	objects map[string][]byte
	bucket  bool
	puts    int
}

func fixtureSignature(r *http.Request) bool {
	a := r.Header.Get("Authorization")
	if !strings.HasPrefix(a, "AWS4-HMAC-SHA256 ") {
		return false
	}
	fields := map[string]string{}
	for _, part := range strings.Split(strings.TrimPrefix(a, "AWS4-HMAC-SHA256 "), ",") {
		k, v, _ := strings.Cut(strings.TrimSpace(part), "=")
		fields[k] = v
	}
	scope := strings.Split(fields["Credential"], "/")
	if len(scope) != 5 || scope[0] != "synthetic-s3-key" || scope[2] != "us-east-1" || scope[3] != "s3" {
		return false
	}
	var headers strings.Builder
	for _, name := range strings.Split(fields["SignedHeaders"], ";") {
		value := r.Header.Get(name)
		if name == "host" {
			value = r.Host
		}
		headers.WriteString(name + ":" + strings.Join(strings.Fields(value), " ") + "\n")
	}
	canonical := r.Method + "\n" + r.URL.EscapedPath() + "\n" + strings.ReplaceAll(r.URL.Query().Encode(), "+", "%20") + "\n" + headers.String() + "\n" + fields["SignedHeaders"] + "\n" + r.Header.Get("X-Amz-Content-Sha256")
	hash := sha256.Sum256([]byte(canonical))
	toSign := "AWS4-HMAC-SHA256\n" + r.Header.Get("X-Amz-Date") + "\n" + strings.Join(scope[1:], "/") + "\n" + hex.EncodeToString(hash[:])
	mac := func(key []byte, value string) []byte {
		h := hmac.New(sha256.New, key)
		h.Write([]byte(value))
		return h.Sum(nil)
	}
	key := []byte("AWS4synthetic-s3-secret")
	for _, part := range scope[1:] {
		key = mac(key, part)
	}
	expected := hex.EncodeToString(mac(key, toSign))
	return hmac.Equal([]byte(expected), []byte(fields["Signature"]))
}

func (f *s3Fixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	failure := func(code string, status int) {
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(status)
		fmt.Fprintf(w, "<Error><Code>%s</Code><Message>synthetic error</Message><Resource>%s</Resource></Error>", code, r.URL.Path)
	}
	if !fixtureSignature(r) {
		failure("SignatureDoesNotMatch", 403)
		return
	}
	if !f.bucket {
		failure("NoSuchBucket", 404)
		return
	}
	if r.URL.Path != "/srics-test" && !strings.HasPrefix(r.URL.Path, "/srics-test/") {
		failure("NoSuchBucket", 404)
		return
	}
	key := strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, "/srics-test"), "/")
	if key == "" {
		if r.Method == "HEAD" {
			return
		}
		if r.Method != "GET" {
			failure("AccessDenied", 403)
			return
		}
		if r.URL.Query().Has("location") {
			io.WriteString(w, `<LocationConstraint xmlns="http://s3.amazonaws.com/doc/2006-03-01/">us-east-1</LocationConstraint>`)
			return
		}
		type object struct {
			Key          string
			LastModified string
			ETag         string
			Size         int
		}
		result := struct {
			XMLName           xml.Name `xml:"ListBucketResult"`
			Xmlns             string   `xml:"xmlns,attr"`
			Name, Prefix      string
			KeyCount, MaxKeys int
			IsTruncated       bool
			Contents          []object
		}{Xmlns: "http://s3.amazonaws.com/doc/2006-03-01/", Name: "srics-test", Prefix: r.URL.Query().Get("prefix"), MaxKeys: 1000}
		keys := []string{}
		for k := range f.objects {
			if strings.HasPrefix(k, result.Prefix) {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		for _, k := range keys {
			b := f.objects[k]
			result.Contents = append(result.Contents, object{k, "2026-01-01T00:00:00.000Z", fmt.Sprintf("\"%x\"", md5.Sum(b)), len(b)})
		}
		result.KeyCount = len(result.Contents)
		w.Header().Set("Content-Type", "application/xml")
		xml.NewEncoder(w).Encode(result)
		return
	}
	switch r.Method {
	case "PUT":
		b, err := io.ReadAll(io.LimitReader(r.Body, 20<<20))
		if err != nil {
			failure("InternalError", 500)
			return
		}
		f.objects[key] = b
		f.puts++
		w.Header().Set("ETag", fmt.Sprintf("\"%x\"", md5.Sum(b)))
	case "GET", "HEAD":
		b, ok := f.objects[key]
		if !ok {
			failure("NoSuchKey", 404)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("ETag", fmt.Sprintf("\"%x\"", md5.Sum(b)))
		w.Header().Set("Content-Length", strconv.Itoa(len(b)))
		http.ServeContent(w, r, key, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), bytes.NewReader(b))
	case "DELETE":
		delete(f.objects, key)
		w.WriteHeader(204)
	default:
		failure("NotImplemented", 501)
	}
}

func TestS3EncryptedBackupAndIndependentRestore(t *testing.T) {
	binary, err := exec.LookPath("restic")
	if err != nil {
		t.Skip("restic unavailable")
	}
	fixture := &s3Fixture{objects: map[string][]byte{}, bucket: true}
	s := httptest.NewTLSServer(fixture)
	defer s.Close()
	root := t.TempDir()
	ca := filepath.Join(root, "ca.pem")
	if err = os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: s.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	connection := S3Config{Endpoint: s.URL, Region: "us-east-1", Bucket: "srics-test", Prefix: "srics/main", Lookup: "path", CAFile: ca}
	c := Client{Binary: binary, Repository: connection.Repository(), Password: "synthetic-cloud-backup-password", S3: &connection, Credentials: Credentials{AccessKeyID: "synthetic-s3-key", SecretAccessKey: "synthetic-s3-secret"}}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	state, err := c.ProbeCloud(ctx)
	if err != nil || state != "empty" {
		t.Fatal("empty probe", state, err)
	}
	fixture.mu.Lock()
	fixture.objects["srics/main/other.txt"] = []byte("unrelated")
	fixture.mu.Unlock()
	if err = c.PrepareCloud(ctx, false); err == nil {
		t.Fatal("nonempty prefix accepted")
	}
	fixture.mu.Lock()
	delete(fixture.objects, "srics/main/other.txt")
	fixture.bucket = false
	fixture.mu.Unlock()
	if err = c.PrepareCloud(ctx, false); err == nil {
		t.Fatal("missing bucket accepted")
	}
	fixture.mu.Lock()
	fixture.bucket = true
	if fixture.puts != 0 {
		t.Fatal("read-only probe wrote objects")
	}
	fixture.mu.Unlock()
	wrong := c
	wrong.Credentials.SecretAccessKey = "incorrect-secret"
	if _, err = wrong.ProbeCloud(ctx); err == nil {
		t.Fatal("wrong signature accepted")
	}
	if err = c.PrepareCloud(ctx, true); err == nil {
		t.Fatal("lost repository recreated")
	}
	if err = c.PrepareCloud(ctx, false); err != nil {
		t.Fatal("initialize", err)
	}
	state, err = c.ProbeCloud(ctx)
	if err != nil || state != "ready" {
		t.Fatal("initialized probe", state, err)
	}
	wrong = c
	wrong.Password = "incorrect-backup-password"
	if err = wrong.PrepareCloud(ctx, false); err == nil {
		t.Fatal("wrong password accepted")
	}
	stage := filepath.Join(root, "stage")
	if err = os.Mkdir(stage, 0700); err != nil {
		t.Fatal(err)
	}
	plain := []byte("synthetic-private-content-never-uploaded-in-plaintext")
	if err = os.WriteFile(filepath.Join(stage, "synthetic-secret-name.txt"), plain, 0600); err != nil {
		t.Fatal(err)
	}
	id, err := c.BackupLibrary(ctx, stage)
	if err != nil {
		t.Fatal("backup", err)
	}
	if err = c.Check(ctx); err != nil {
		t.Fatal("check", err)
	}
	fixture.mu.Lock()
	for _, b := range fixture.objects {
		if bytes.Contains(b, plain) || bytes.Contains(b, []byte("synthetic-secret-name.txt")) {
			t.Fatal("plaintext uploaded to S3")
		}
	}
	fixture.mu.Unlock()
	if err = os.RemoveAll(stage); err != nil {
		t.Fatal(err)
	}
	restored := filepath.Join(root, "restored")
	if err = c.Restore(ctx, id, restored); err != nil {
		t.Fatal("restore", err)
	}
	b, err := os.ReadFile(filepath.Join(restored, "synthetic-secret-name.txt"))
	if err != nil || !bytes.Equal(b, plain) {
		t.Fatal("restore bytes", err)
	}
	if err = c.Restore(ctx, id, restored); err == nil {
		t.Fatal("existing restore directory overwritten")
	}
}
