package backup

import (
	"os"
	"path/filepath"
	"testing"
)

func TestUnifiedPathsRejectAliasesAndOverlap(t *testing.T) {
	root := t.TempDir()
	data := filepath.Join(root, "library")
	os.Mkdir(data, 0700)
	alias := filepath.Join(root, "alias")
	os.Symlink(data, alias)
	c := UnifiedConfig{Repository: filepath.Join(root, "cache", "repository"), PasswordFile: filepath.Join(root, "cache", "key"), Directory: filepath.Join(root, "output")}
	if err := c.Validate(data); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{data, filepath.Join(alias, "backup"), root, c.Repository, filepath.Dir(c.PasswordFile)} {
		bad := c
		bad.Directory = dir
		if err := bad.Validate(data); err == nil {
			t.Fatal("overlap accepted", dir)
		}
	}
}
