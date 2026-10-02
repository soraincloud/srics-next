package main

import (
	"context"
	"encoding/json"
	"github.com/soraincloud/srics-next/internal/atomicfile"
	"github.com/soraincloud/srics-next/internal/backup"
	"github.com/soraincloud/srics-next/internal/buildinfo"
	"github.com/soraincloud/srics-next/internal/library"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func prepareUnifiedUpdate(ctx context.Context, path string, c localConfig, binary string) (string, error) {
	if err := stopManaged(ctx, path); err != nil {
		return "", err
	}
	l, err := library.Open(c.Data)
	if err != nil {
		return "", err
	}
	defer l.Close()
	plan := backup.Unified{Config: *c.Unified, Binary: binary, ProtectedDirectory: filepath.Dir(path)}
	result, err := plan.Run(ctx, l, "")
	if e := saveUnifiedResult(l, plan, result, err); e != nil {
		return "", e
	}
	if err != nil {
		return "", err
	}
	dir := filepath.Join(filepath.Dir(path), "updates", time.Now().Format("20060102-150405")+"-"+library.NewID()[:6])
	if err = os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	record := updateRecord{Version: version, Release: buildinfo.Current(), Time: time.Now().UTC(), Target: "unified", Snapshot: result.Snapshot}
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	app := strings.TrimSuffix(exe, string(os.PathSeparator)+filepath.Join("Contents", "Resources", "bin", "srics"))
	if strings.HasSuffix(app, ".app") && app != exe {
		record.PreviousApp = filepath.Join(dir, "SRICS Next.app")
		if err = exec.CommandContext(ctx, "/usr/bin/ditto", app, record.PreviousApp).Run(); err != nil {
			return "", err
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
