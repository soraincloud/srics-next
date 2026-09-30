package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/soraincloud/srics-next/internal/atomicfile"
	"github.com/soraincloud/srics-next/internal/buildinfo"
	"github.com/soraincloud/srics-next/internal/library"
	"github.com/soraincloud/srics-next/internal/verification"
)

func loginAgentPath(path string) (string, error) {
	home, err := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", serviceLabel(path)+".plist"), err
}
func syncLoginAgent(path string, c localConfig, executable string) error {
	if runtime.GOOS != "darwin" {
		return nil
	}
	dest, err := loginAgentPath(path)
	if err != nil {
		return err
	}
	return writeLoginAgent(dest, path, c, executable)
}
func writeLoginAgent(dest, path string, c localConfig, executable string) error {
	var err error
	if !c.AutoStart {
		err = os.Remove(dest)
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if err = os.MkdirAll(filepath.Dir(dest), 0700); err != nil {
		return err
	}
	tmp := dest + "." + library.NewID()
	defer os.Remove(tmp)
	data := launchPlist(path, executable, managerStatus{Config: c, Log: filepath.Join(filepath.Dir(path), "service.log")})
	if err = atomicfile.WriteNew(tmp, func(w io.Writer) error { _, e := io.WriteString(w, data); return e }); err != nil {
		return err
	}
	return os.Rename(tmp, dest)
}

type updateRecord struct {
	Version     string         `json:"version"`
	Release     buildinfo.Info `json:"release"`
	Time        time.Time      `json:"time"`
	Target      string         `json:"target"`
	Snapshot    string         `json:"snapshot"`
	PreviousApp string         `json:"previousApp"`
}

func prepareUpdate(ctx context.Context, path string) (string, error) {
	c, _, err := loadConfig(path)
	if err != nil {
		return "", err
	}
	tools := verification.ResolveTools()
	client, err := configuredBackup(c, tools.Restic)
	target := "local"
	if err != nil {
		return "", err
	}
	if client.Repository == "" {
		client, err = configuredCloud(c, tools.Restic)
		target = "cloud"
	}
	if err != nil {
		return "", err
	}
	if client.Repository == "" {
		return "", errors.New("请先配置并完成一次加密备份，再准备更新")
	}
	if err = stopManaged(ctx, path); err != nil {
		return "", err
	}
	l, err := library.Open(c.Data)
	if err != nil {
		return "", err
	}
	defer l.Close()
	// Existing, readable repositories only. Updating must never initialize a replacement.
	if _, err = client.LibrarySnapshots(ctx); err != nil {
		return "", err
	}
	stage := filepath.Join(l.Root, "staging", "update-"+library.NewID())
	defer os.RemoveAll(stage)
	if err = l.Snapshot(ctx, stage); err != nil {
		return "", err
	}
	pinned, err := library.Open(stage)
	if err != nil {
		return "", err
	}
	err = pinned.Verify(ctx)
	pinned.Close()
	if err != nil {
		return "", err
	}
	id, err := client.BackupLibrary(ctx, stage)
	if err != nil {
		return "", err
	}
	if err = client.Check(ctx); err != nil {
		return "", err
	}
	dir := filepath.Join(filepath.Dir(path), "updates", time.Now().Format("20060102-150405")+"-"+library.NewID()[:6])
	if err = os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	record := updateRecord{Version: version, Release: buildinfo.Current(), Time: time.Now().UTC(), Target: target, Snapshot: id}
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	suffix := filepath.Join("Contents", "Resources", "bin", "srics")
	app := strings.TrimSuffix(exe, string(os.PathSeparator)+suffix)
	if strings.HasSuffix(app, ".app") && app != exe {
		record.PreviousApp = filepath.Join(dir, "SRICS Next.app")
		if err = exec.CommandContext(ctx, "/usr/bin/ditto", app, record.PreviousApp).Run(); err != nil {
			return "", fmt.Errorf("备份已完成，但保留旧程序失败：%w", err)
		}
	}
	if err = atomicfile.CopyNew(filepath.Join(dir, "config.json"), path); err != nil {
		return "", err
	}
	if err = atomicfile.WriteNew(filepath.Join(dir, "update.json"), func(w io.Writer) error { return json.NewEncoder(w).Encode(record) }); err != nil {
		return "", err
	}
	return dir, nil
}
