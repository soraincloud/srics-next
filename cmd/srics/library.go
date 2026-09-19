package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/soraincloud/srics-next/internal/atomicfile"
	"github.com/soraincloud/srics-next/internal/backup"
	"github.com/soraincloud/srics-next/internal/library"
)

// The installation marker lives in the OS configuration directory, separately
// from the default data directory. Once initialized, missing data is an error.
func openLibrary(path string) (*library.Library, error) {
	if path != "" {
		return library.Open(path)
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return nil, err
	}
	base = filepath.Join(base, "SRICS Next")
	if err = os.MkdirAll(base, 0700); err != nil {
		return nil, err
	}
	root := filepath.Join(base, "library")
	marker := filepath.Join(base, "initialized")
	if _, err = os.Stat(marker); os.IsNotExist(err) {
		if _, err = os.Stat(root); os.IsNotExist(err) {
			if err = library.Create(root); err != nil {
				return nil, err
			}
		}
		l, e := library.Open(root)
		if e != nil {
			return nil, e
		}
		if e = atomicfile.WriteNew(marker, func(w io.Writer) error { _, e := io.WriteString(w, "srics-library-v1\n"); return e }); e != nil {
			l.Close()
			return nil, e
		}
		return l, nil
	} else if err != nil {
		return nil, err
	}
	return library.Open(root)
}
func backupConfig(binary string) (backup.Client, error) {
	c := backup.Client{Binary: binary, Repository: os.Getenv("SRICS_BACKUP_REPOSITORY")}
	passfile := os.Getenv("SRICS_BACKUP_PASSWORD_FILE")
	if c.Repository == "" && passfile == "" {
		return c, nil
	}
	if !filepath.IsAbs(c.Repository) || passfile == "" {
		return c, errors.New("备份需要绝对仓库路径和 SRICS_BACKUP_PASSWORD_FILE")
	}
	b, err := os.ReadFile(passfile)
	if err != nil {
		return c, errors.New("无法读取备份口令文件")
	}
	c.Password = strings.TrimRight(string(b), "\r\n")
	clear(b)
	if len(c.Password) < 12 {
		return c, errors.New("备份口令至少需要 12 字节")
	}
	return c, nil
}
func restoreLibrary(ctx context.Context, args []string, binary string) error {
	flags := flag.NewFlagSet("restore", flag.ContinueOnError)
	repo := flags.String("repo", "", "absolute restic repository")
	passwordFile := flags.String("password-file", "", "independent backup password file")
	snapshot := flags.String("snapshot", "", "full snapshot id")
	target := flags.String("target", "", "new restore directory")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *target == "" {
		return errors.New("恢复需要 --repo --password-file --snapshot --target；目标必须不存在")
	}
	pass, err := os.ReadFile(*passwordFile)
	if err != nil {
		return err
	}
	c := backup.Client{Binary: binary, Repository: *repo, Password: strings.TrimRight(string(pass), "\r\n")}
	clear(pass)
	if err = c.Restore(ctx, *snapshot, *target); err != nil {
		return err
	}
	l, err := library.Open(*target)
	if err != nil {
		return err
	}
	defer l.Close()
	return l.Verify(ctx)
}
