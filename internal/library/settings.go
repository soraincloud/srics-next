package library

import (
	"encoding/hex"
	"encoding/json"
	"github.com/soraincloud/srics-next/internal/atomicfile"
	"io"
	"path/filepath"
	"strings"
)

// Called under the snapshot mutation lock. This manifest contains only public
// key fingerprints, so restore-point lists can indicate supported recovery keys.
func (l *Library) recoveryManifest(dest string) error {
	wrapped, err := l.Setting("vault-key")
	if err != nil {
		return err
	}
	rows, err := l.db.Query("SELECT key,value FROM settings WHERE key LIKE 'recovery-key-%' ORDER BY key")
	if err != nil {
		return err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var name string
		var data []byte
		if err = rows.Scan(&name, &data); err != nil {
			return err
		}
		id := strings.TrimPrefix(name, "recovery-key-")
		if b, e := hex.DecodeString(id); e == nil && len(b) == 32 {
			var record struct {
				Info struct {
					ID           string `json:"id"`
					VaultPresent bool   `json:"vaultPresent"`
				} `json:"info"`
				WrappedVault []byte `json:"wrappedVault"`
			}
			if json.Unmarshal(data, &record) != nil || record.Info.ID != id {
				continue
			}
			if record.Info.VaultPresent != (len(wrapped) > 0) || (record.Info.VaultPresent && len(record.WrappedVault) == 0) {
				continue
			}
			ids = append(ids, id)
		}
	}
	if err = rows.Err(); err != nil {
		return err
	}
	return atomicfile.WriteNew(filepath.Join(dest, "recovery-keys.json"), func(w io.Writer) error { return json.NewEncoder(w).Encode(ids) })
}

// SetSettings publishes related settings together, including credentials during
// emergency recovery. A failed write must not leave only one password changed.
func (l *Library) SetSettings(values map[string][]byte) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.Check(); err != nil {
		return err
	}
	tx, err := l.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for key, value := range values {
		if _, err = tx.Exec("INSERT INTO settings(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", key, value); err != nil {
			return err
		}
	}
	return tx.Commit()
}
