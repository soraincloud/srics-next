package library

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDamagedRecoveryRecordStopsSnapshot(t *testing.T) {
	for _, damage := range []string{"invalid-json", "missing-wrapper", "missing-active-record"} {
		t.Run(damage, func(t *testing.T) {
			l := testLibrary(t)
			id := strings.Repeat("a", 64)
			values := map[string][]byte{
				"vault-key":                 []byte("synthetic-wrapped-vault"),
				"recovery-library-id":       []byte(NewID()),
				"recovery-key-active-local": []byte(id),
			}
			switch damage {
			case "invalid-json":
				values["recovery-key-"+id] = []byte("{broken")
			case "missing-wrapper":
				values["recovery-key-"+id], _ = json.Marshal(map[string]any{"info": map[string]any{"id": id, "vaultPresent": true}})
			}
			if err := l.SetSettings(values); err != nil {
				t.Fatal(err)
			}
			stage := filepath.Join(l.Root, "staging", "damaged-recovery")
			if err := l.Snapshot(context.Background(), stage); err == nil {
				t.Fatal("damaged recovery record was silently omitted from a successful snapshot")
			}
			if _, err := os.Stat(stage); !os.IsNotExist(err) {
				t.Fatal("failed snapshot left a publishable stage")
			}
		})
	}
}
