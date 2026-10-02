package backup

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func packageFixture(t *testing.T) (string, string, PackageManifest) {
	t.Helper()
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	id := strings.Repeat("a", 64)
	for _, dir := range []string{"snapshots", "keys", "index", "data/aa", "locks"} {
		if err := os.MkdirAll(filepath.Join(repo, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"config", "snapshots/" + id, "keys/" + id, "data/aa/" + id} {
		if err := os.WriteFile(filepath.Join(repo, name), []byte("encrypted-fixture-"+name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return repo, filepath.Join(root, "backup.sricsbackup"), PackageManifest{RepositoryID: id, Snapshot: id, RecoveryKeyID: id}
}

func TestPackageRoundTripAndCancellation(t *testing.T) {
	repo, file, m := packageFixture(t)
	if err := os.WriteFile(filepath.Join(repo, ".DS_Store"), []byte("Finder metadata"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := ExportPackage(context.Background(), repo, file, m); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(file)
	if info.Mode().Perm() != 0600 {
		t.Fatal("package permissions")
	}
	zr, err := zip.OpenReader(file)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	for _, f := range zr.File {
		if f.Method != zip.Store {
			t.Fatal("ciphertext recompressed")
		}
	}
	dest := filepath.Join(filepath.Dir(file), "imported")
	got, err := ImportPackage(context.Background(), file, dest)
	if err != nil {
		t.Fatal(err)
	}
	if got.Snapshot != m.Snapshot || len(got.Files) != 4 {
		t.Fatal("manifest incomplete")
	}
	for _, entry := range got.Files {
		old, _ := os.ReadFile(filepath.Join(repo, entry.Name))
		fresh, _ := os.ReadFile(filepath.Join(dest, "repository", entry.Name))
		if !bytes.Equal(old, fresh) {
			t.Fatal("encrypted bytes changed")
		}
	}
	if _, err := ImportPackage(context.Background(), file, dest); err == nil {
		t.Fatal("existing import replaced")
	}
	if err := ExportPackage(context.Background(), repo, file, m); err == nil {
		t.Fatal("existing export replaced")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	unfinished := filepath.Join(filepath.Dir(file), "cancelled.sricsbackup")
	if err := ExportPackage(ctx, repo, unfinished, m); err == nil {
		t.Fatal("cancelled export succeeded")
	}
	if _, err := os.Stat(unfinished); !os.IsNotExist(err) {
		t.Fatal("partial export published")
	}
	canceledDest := filepath.Join(filepath.Dir(file), "cancelled-import")
	if _, err := ImportPackage(ctx, file, canceledDest); err == nil {
		t.Fatal("cancelled import succeeded")
	}
	if _, err := os.Stat(canceledDest); !os.IsNotExist(err) {
		t.Fatal("cancelled import retained")
	}
}

func TestPackageRejectsUnknownOrActiveSource(t *testing.T) {
	for _, kind := range []string{"extra", "symlink", "lock", "wrong-shard"} {
		t.Run(kind, func(t *testing.T) {
			repo, file, m := packageFixture(t)
			var err error
			switch kind {
			case "extra":
				err = os.WriteFile(filepath.Join(repo, "recovery.json"), []byte("never include"), 0600)
			case "symlink":
				err = os.Symlink(filepath.Join(repo, "config"), filepath.Join(repo, "keys", strings.Repeat("b", 64)))
			case "lock":
				err = os.WriteFile(filepath.Join(repo, "locks", "lock"), []byte("active"), 0600)
			case "wrong-shard":
				err = os.WriteFile(filepath.Join(repo, "data", "aa", strings.Repeat("b", 64)), []byte("wrong"), 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = ExportPackage(context.Background(), repo, file, m); err == nil {
				t.Fatal("unsafe source accepted")
			}
		})
	}
}

func TestPackageRejectsMaliciousAndCorruptArchives(t *testing.T) {
	repo, file, m := packageFixture(t)
	if err := ExportPackage(context.Background(), repo, file, m); err != nil {
		t.Fatal(err)
	}
	zr, err := zip.OpenReader(file)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	for _, kind := range []string{"traversal", "absolute", "duplicate", "symlink", "compressed", "hash", "size", "version", "missing", "extra", "oversized", "truncated"} {
		t.Run(kind, func(t *testing.T) {
			target := filepath.Join(t.TempDir(), "bad.sricsbackup")
			var buffer bytes.Buffer
			zw := zip.NewWriter(&buffer)
			for i, entry := range zr.File {
				if kind == "missing" && i == 0 {
					continue
				}
				r, e := entry.Open()
				if e != nil {
					t.Fatal(e)
				}
				data, e := io.ReadAll(r)
				r.Close()
				if e != nil {
					t.Fatal(e)
				}
				h := entry.FileHeader
				if i == 0 {
					switch kind {
					case "traversal":
						h.Name = "repository/../outside"
					case "absolute":
						h.Name = "/tmp/outside"
					case "symlink":
						h.SetMode(os.ModeSymlink | 0600)
					case "compressed":
						h.Method = zip.Deflate
					case "hash":
						data[0] ^= 1
					}
				}
				if h.Name == packageManifestName {
					var altered PackageManifest
					if json.Unmarshal(data, &altered) != nil {
						t.Fatal("manifest")
					}
					switch kind {
					case "size":
						altered.Files[0].Size++
					case "version":
						altered.Version++
					case "oversized":
						altered.Files[0].Size = maxPackageBytes + 1
					}
					data, _ = json.Marshal(altered)
				}
				w, e := zw.CreateHeader(&h)
				if e != nil {
					t.Fatal(e)
				}
				if _, e = w.Write(data); e != nil {
					t.Fatal(e)
				}
				if kind == "duplicate" && i == 0 {
					duplicate := h
					w, e := zw.CreateHeader(&duplicate)
					if e != nil {
						t.Fatal(e)
					}
					w.Write(data)
				}
			}
			if kind == "extra" {
				w, _ := zw.Create("repository/recovery.json")
				w.Write([]byte("secret"))
			}
			if err := zw.Close(); err != nil {
				t.Fatal(err)
			}
			data := buffer.Bytes()
			if kind == "truncated" {
				data = data[:len(data)/2]
			}
			if err := os.WriteFile(target, data, 0600); err != nil {
				t.Fatal(err)
			}
			dest := filepath.Join(filepath.Dir(target), "imported")
			if _, err := ImportPackage(context.Background(), target, dest); err == nil {
				t.Fatal("unsafe archive accepted")
			}
			if _, err := os.Stat(dest); !os.IsNotExist(err) {
				t.Fatal("invalid import left directory")
			}
		})
	}
}

func TestPackageZIP64MetadataAndAllocationBounds(t *testing.T) {
	repo, file, m := packageFixture(t)
	if err := ExportPackage(context.Background(), repo, file, m); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	end := original[len(original)-22:]
	for _, kind := range []string{"zip64", "too-many", "large-directory", "bad-offset", "trailing", "multi-disk"} {
		t.Run(kind, func(t *testing.T) {
			data := append([]byte(nil), original[:len(original)-22]...)
			footer := append([]byte(nil), end...)
			var extended [56]byte
			binary.LittleEndian.PutUint32(extended[:], 0x06064b50)
			binary.LittleEndian.PutUint64(extended[4:], 44)
			count := uint64(binary.LittleEndian.Uint16(end[10:]))
			size := uint64(binary.LittleEndian.Uint32(end[12:]))
			offset := uint64(binary.LittleEndian.Uint32(end[16:]))
			if kind == "too-many" {
				count = maxPackageFiles + 2
			}
			if kind == "large-directory" {
				size = 129 << 20
			}
			if kind == "bad-offset" {
				offset = ^uint64(0)
			}
			binary.LittleEndian.PutUint64(extended[24:], count)
			binary.LittleEndian.PutUint64(extended[32:], count)
			binary.LittleEndian.PutUint64(extended[40:], size)
			binary.LittleEndian.PutUint64(extended[48:], offset)
			var locator [20]byte
			binary.LittleEndian.PutUint32(locator[:], 0x07064b50)
			binary.LittleEndian.PutUint64(locator[8:], uint64(len(data)))
			binary.LittleEndian.PutUint32(locator[16:], 1)
			data = append(data, extended[:]...)
			data = append(data, locator[:]...)
			binary.LittleEndian.PutUint16(footer[8:], 65535)
			binary.LittleEndian.PutUint16(footer[10:], 65535)
			if kind == "multi-disk" {
				binary.LittleEndian.PutUint16(footer[4:], 1)
			}
			data = append(data, footer...)
			if kind == "trailing" {
				data = append(data, 'x')
			}
			target := filepath.Join(t.TempDir(), "zip64.sricsbackup")
			if err := os.WriteFile(target, data, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := InspectPackage(target); (kind == "zip64") != (err == nil) {
				t.Fatal(kind, err)
			}
			if kind == "zip64" {
				if _, err := ImportPackage(context.Background(), target, filepath.Join(filepath.Dir(target), "import")); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
