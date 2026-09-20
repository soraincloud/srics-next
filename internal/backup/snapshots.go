package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sort"
	"time"
)

type limitedOutput struct {
	buffer    *bytes.Buffer
	remaining int
}

func (w *limitedOutput) Write(p []byte) (int, error) {
	if len(p) > w.remaining {
		return 0, errors.New("备份历史过大")
	}
	w.remaining -= len(p)
	return w.buffer.Write(p)
}

type Snapshot struct {
	ID    string    `json:"id"`
	Time  time.Time `json:"time"`
	Files *uint64   `json:"files,omitempty"`
	Bytes *uint64   `json:"bytes,omitempty"`
}

// LibrarySnapshots exposes only metadata, never backup paths, usernames or tags.
func (c Client) LibrarySnapshots(ctx context.Context) ([]Snapshot, error) {
	data, err := c.runLimited(ctx, "", 16<<20, "snapshots", "--json", "--host", "srics-library", "--tag", "library-v1")
	if err != nil {
		return nil, errors.New("无法读取备份历史，请检查目标连接、口令和读取权限")
	}
	return decodeSnapshots(data)
}
func decodeSnapshots(data []byte) ([]Snapshot, error) {
	var raw []struct {
		ID      string    `json:"id"`
		Time    time.Time `json:"time"`
		Host    string    `json:"hostname"`
		Tags    []string  `json:"tags"`
		Summary *struct {
			Files *uint64 `json:"total_files_processed"`
			Bytes *uint64 `json:"total_bytes_processed"`
		} `json:"summary"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, errors.New("备份历史格式无法识别")
	}
	out := make([]Snapshot, 0, len(raw))
	seen := map[string]bool{}
	for _, v := range raw {
		if v.Host != "srics-library" || !slices.Contains(v.Tags, "library-v1") {
			continue
		}
		if !snapshotID.MatchString(v.ID) || v.Time.IsZero() || seen[v.ID] {
			return nil, errors.New("备份历史包含无效快照")
		}
		seen[v.ID] = true
		s := Snapshot{ID: v.ID, Time: v.Time}
		if v.Summary != nil {
			s.Files, s.Bytes = v.Summary.Files, v.Summary.Bytes
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Time.Equal(out[j].Time) {
			return out[i].ID > out[j].ID
		}
		return out[i].Time.After(out[j].Time)
	})
	return out, nil
}
