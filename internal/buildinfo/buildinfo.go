// Package buildinfo provides one release identity for the service, UI and app bundle.
package buildinfo

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"runtime/debug"
)

//go:embed release.json
var releaseJSON []byte

// BuiltAt is set to an RFC3339 UTC timestamp by scripts/build.sh.
var BuiltAt string

type Info struct {
	Version string `json:"version"`
	Build   int    `json:"build"`
	Commit  string `json:"commit"`
	Dirty   bool   `json:"dirty"`
	BuiltAt string `json:"builtAt"`
	Label   string `json:"label"`
}

func Current() Info {
	var info Info
	if err := json.Unmarshal(releaseJSON, &info); err != nil || info.Version == "" || info.Build < 1 {
		panic("invalid embedded release.json")
	}
	info.BuiltAt = BuiltAt
	if build, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range build.Settings {
			switch setting.Key {
			case "vcs.revision":
				info.Commit = setting.Value
			case "vcs.modified":
				info.Dirty = setting.Value == "true"
			}
		}
	}
	info.Label = fmt.Sprintf("v%s · Build %d", info.Version, info.Build)
	return info
}
