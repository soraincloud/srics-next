package buildinfo

import "testing"

func TestExportedSourceIdentity(t *testing.T) {
	oldCommit, oldDirty := SourceCommit, SourceDirty
	defer func() { SourceCommit, SourceDirty = oldCommit, oldDirty }()
	SourceCommit, SourceDirty = "synthetic-exported-source", "false"
	info := Current()
	if info.Commit != SourceCommit || info.Dirty {
		t.Fatal("exported source lost its verified identity")
	}
	SourceDirty = "true"
	if !Current().Dirty {
		t.Fatal("modified source export was reported as clean")
	}
}
