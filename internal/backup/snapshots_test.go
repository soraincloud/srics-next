package backup

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestSnapshotMetadataFilteringAndOrdering(t *testing.T) {
	id1, id2 := strings.Repeat("1", 64), strings.Repeat("2", 64)
	data := `[{"id":"` + id1 + `","time":"2026-09-19T10:00:00Z","hostname":"srics-library","tags":["library-v1"],"paths":["sensitive/path"],"username":"sensitive-user","summary":{"total_files_processed":3,"total_bytes_processed":4096}},{"id":"` + id2 + `","time":"2026-09-20T10:00:00Z","hostname":"srics-library","tags":["library-v1"]},{"id":"test","time":"2026-09-20T11:00:00Z","hostname":"srics-verification","tags":["m0"]}]`
	entries, err := decodeSnapshots([]byte(data))
	if err != nil || len(entries) != 2 || entries[0].ID != id2 || entries[0].Bytes != nil || *entries[1].Bytes != 4096 {
		t.Fatal(entries, err)
	}
	encoded, _ := json.Marshal(entries)
	if bytes.Contains(encoded, []byte("sensitive")) || bytes.Contains(encoded, []byte("paths")) {
		t.Fatal("private source metadata exposed")
	}
	for _, bad := range []string{`{}`, `[{"id":"bad","hostname":"srics-library","tags":["library-v1"],"time":"2026-09-20T10:00:00Z"}]`, strings.Replace(data, `"2026-09-20T10:00:00Z"`, `null`, 1)} {
		if _, err = decodeSnapshots([]byte(bad)); err == nil {
			t.Fatal("invalid history accepted")
		}
	}
}
func TestSnapshotOutputIsBounded(t *testing.T) {
	var b bytes.Buffer
	w := limitedOutput{buffer: &b, remaining: 4}
	if _, err := w.Write([]byte("1234")); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("5")); err == nil || b.String() != "1234" {
		t.Fatal("unbounded history output")
	}
}
