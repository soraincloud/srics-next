package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/soraincloud/srics-next/internal/atomicfile"
	"github.com/soraincloud/srics-next/internal/library"
	"github.com/soraincloud/srics-next/internal/vault"
	"golang.org/x/sys/unix"
)

type migrationRequest struct {
	Directory string `json:"directory"`
}

func migrationDestination(path string, c localConfig, directory string) (string, error) {
	dest, err := recoveryDirectory(directory, c.Data, c.BackupRepository, filepath.Dir(path))
	if err != nil {
		return "", errors.New("请选择当前资料库、备份仓库和程序配置目录之外的新目录")
	}
	if _, err = os.Lstat(dest); !os.IsNotExist(err) {
		return "", errors.New("目标目录已存在或无法访问；请选择新目录，不会覆盖已有文件")
	}
	if info, err := os.Stat(filepath.Dir(dest)); err != nil || !info.IsDir() {
		return "", errors.New("目标位置不存在，请先连接目标磁盘")
	}
	proposed := c
	proposed.Data = dest
	if err = proposed.validate(); err != nil {
		return "", err
	}
	return dest, nil
}

// Copy real bytes even on the same volume: the retained original must be an
// independent fallback. Every file is flushed then reread and hash checked.
func copyMigrationFile(ctx context.Context, dest, source string) error {
	f, err := os.Open(source)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("迁移源包含非普通文件")
	}
	before := sha256.New()
	err = atomicfile.WriteNew(dest, func(w io.Writer) error {
		n, e := io.Copy(io.MultiWriter(w, before), vault.ContextReader{Ctx: ctx, Reader: f})
		if e == nil && n != info.Size() {
			e = errors.New("迁移期间源文件大小发生变化")
		}
		return e
	})
	if err != nil {
		return err
	}
	check, err := os.Open(dest)
	if err != nil {
		return err
	}
	defer check.Close()
	after := sha256.New()
	n, err := io.Copy(after, vault.ContextReader{Ctx: ctx, Reader: check})
	if err != nil {
		return err
	}
	if n != info.Size() || !bytes.Equal(before.Sum(nil), after.Sum(nil)) {
		return errors.New("迁移文件校验失败")
	}
	return nil
}
func syncMigrationDirectory(path string) error {
	d, err := os.Open(path)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func copyMigrationSnapshot(ctx context.Context, source, dest string) error {
	var required uint64 = 64 << 20
	err := filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("迁移源包含链接或特殊文件")
		}
		if info.Size() < 0 || uint64(info.Size()) > math.MaxUint64-required {
			return errors.New("迁移资料大小无效")
		}
		required += uint64(info.Size())
		return nil
	})
	if err != nil {
		return err
	}
	var stat unix.Statfs_t
	if err = unix.Statfs(filepath.Dir(dest), &stat); err != nil {
		return err
	}
	if uint64(stat.Bavail)*uint64(stat.Bsize) < required {
		return errors.New("目标磁盘空间不足，需能容纳完整资料库并保留至少 64 MiB 空间")
	}
	// Mkdir is exclusive: no existing directory or symlink is reused.
	if err = os.Mkdir(dest, 0700); err != nil {
		return err
	}
	if err = syncMigrationDirectory(filepath.Dir(dest)); err != nil {
		return err
	}
	dirs := []string{}
	err = filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		out := filepath.Join(dest, rel)
		if entry.IsDir() {
			if rel != "." {
				if err = os.Mkdir(out, 0700); err != nil {
					return err
				}
			}
			dirs = append(dirs, out)
			return nil
		}
		if !entry.Type().IsRegular() {
			return errors.New("迁移源包含链接或特殊文件")
		}
		return copyMigrationFile(ctx, out, path)
	})
	if err != nil {
		return err
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		if err = syncMigrationDirectory(dirs[i]); err != nil {
			return err
		}
	}
	return nil
}

func migrateLibrary(ctx context.Context, path, directory string) error {
	c, saved, err := loadConfig(path)
	if err != nil {
		return err
	}
	if !saved {
		return errors.New("请先完成资料库配置")
	}
	dest, err := migrationDestination(path, c, directory)
	if err != nil {
		return err
	}
	if runtime.GOOS == "darwin" && serviceLoaded(ctx, path) {
		return errors.New("请先停止服务再迁移资料库")
	}
	source, err := library.Open(c.Data)
	if err != nil {
		return fmt.Errorf("无法独占当前资料库，未迁移：%w", err)
	}
	defer source.Close()
	// Snapshot produces a coherent SQLite database, includes trash, private
	// ciphertext and resumable upload chunks, and validates recovery key records.
	stage := filepath.Join(source.Root, "staging", "migration-"+library.NewID())
	if err = source.Snapshot(ctx, stage); err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	if err = copyMigrationSnapshot(ctx, stage, dest); err != nil {
		return fmt.Errorf("复制未完成，仍使用原目录；目标中的副本保留，重试请选择新目录：%w", err)
	}
	target, err := library.Open(dest)
	if err != nil {
		return fmt.Errorf("副本无法打开，未切换资料目录：%w", err)
	}
	defer target.Close()
	if err = target.Verify(ctx); err != nil {
		return fmt.Errorf("副本索引或文件校验失败，未切换资料目录：%w", err)
	}
	password, err := target.Setting("password")
	if err != nil || len(password) == 0 {
		return errors.New("副本缺少登录密码，未切换资料目录")
	}
	if err = source.Check(); err != nil {
		return err
	}
	// Keep the original configuration alongside a durable migration receipt.
	recordDir := filepath.Join(filepath.Dir(path), "migrations")
	if err = os.MkdirAll(recordDir, 0700); err != nil {
		return err
	}
	if err = syncMigrationDirectory(filepath.Dir(path)); err != nil {
		return err
	}
	id := library.NewID()
	if err = atomicfile.CopyNew(filepath.Join(recordDir, id+"-config.json"), path); err != nil {
		return err
	}
	record := struct {
		Source, Destination string
		VerifiedAt          time.Time
	}{c.Data, dest, time.Now().UTC()}
	if err = atomicfile.WriteNew(filepath.Join(recordDir, id+".json"), func(w io.Writer) error { return json.NewEncoder(w).Encode(record) }); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	c.Data = dest
	if err = writeConfig(path, c); err != nil {
		return fmt.Errorf("副本校验已通过，但配置保存未确认；请刷新状态查看当前目录，两个目录均已保留：%w", err)
	}
	// LaunchAgents use the unchanged --config path; no credentials or TLS change.
	return nil
}

func migrationManager(ctx context.Context, path string, input io.Reader) error {
	var req migrationRequest
	d := json.NewDecoder(io.LimitReader(input, 16385))
	d.DisallowUnknownFields()
	if d.Decode(&req) != nil || d.Decode(&struct{}{}) != io.EOF {
		return errors.New("迁移请求格式不正确")
	}
	c, saved, err := loadConfig(path)
	if err != nil {
		return err
	}
	if !saved {
		return errors.New("请先完成资料库配置")
	}
	if _, err = migrationDestination(path, c, req.Directory); err != nil {
		return err
	}
	if err = stopManaged(ctx, path); err != nil {
		return err
	}
	return migrateLibrary(ctx, path, req.Directory)
}
