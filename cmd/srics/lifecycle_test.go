package main

import (
	"github.com/soraincloud/srics-next/internal/backup"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLifecycleDefaultsAndScopedLaunchAgent(t *testing.T) {
	var c localConfig
	if c.AutoStart || c.AutoRestart || c.TrashDays != 0 || c.Retention.Enabled {
		t.Fatal("destructive or persistent defaults enabled")
	}
	plist := launchPlist("/tmp/a&b/config.json", "/tmp/a&b/srics", managerStatus{Config: localConfig{AutoRestart: true}})
	for _, want := range []string{"KeepAlive", "SuccessfulExit", "ThrottleInterval", "a&amp;b"} {
		if !strings.Contains(plist, want) {
			t.Fatal(want)
		}
	}
	path := filepath.Join(t.TempDir(), "config.json")
	c.AutoStart = true
	dest := filepath.Join(t.TempDir(), serviceLabel(path)+".plist")
	if err := writeLoginAgent(dest, path, c, "/tmp/test-srics"); err != nil {
		t.Fatal(err)
	}
	b, e := os.ReadFile(dest)
	if e != nil || !strings.Contains(string(b), serviceLabel(path)) {
		t.Fatal(e)
	}
	unrelated := filepath.Join(filepath.Dir(dest), "unrelated.plist")
	os.WriteFile(unrelated, []byte("untouched"), 0600)
	c.AutoStart = false
	if err := writeLoginAgent(dest, path, c, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatal("agent left behind")
	}
	if _, err := os.Stat(unrelated); err != nil {
		t.Fatal("unrelated agent changed")
	}
}
func TestInvalidCleanupConfiguration(t *testing.T) {
	c := localConfig{Data: filepath.Join(t.TempDir(), "library"), Port: 19473, TrashDays: -1}
	if c.validate() == nil {
		t.Fatal("negative trash policy")
	}
	c.TrashDays = 30
	c.Retention = backup.Retention{Enabled: true}
	if c.validate() == nil {
		t.Fatal("zero retention")
	}
	c.Retention.Daily = 30
	if err := c.validate(); err != nil {
		t.Fatal(err)
	}
}
