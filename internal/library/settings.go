package library

import (
	"encoding/json"
	"errors"
	"github.com/soraincloud/srics-next/internal/atomicfile"
	"io"
	"path/filepath"
	"strings"
	"time"
)

// Called under the snapshot mutation lock. This manifest contains only public
// key fingerprints, so restore-point lists can indicate supported recovery keys.
func (l *Library) recoveryManifest(dest string) error {
	wrapped, err := l.Setting("vault-key")
	if err != nil {
		return err
	}
	libraryID, err := l.Setting("recovery-library-id")
	if err != nil {
		return err
	}
	invalid := errors.New("恢复密钥记录损坏，已停止备份；请保留已有备份并检查恢复文件")
	active := []string{}
	for _, target := range []string{"local", "cloud"} {
		id, err := l.Setting("recovery-key-active-" + target)
		if err != nil {
			return err
		}
		if len(id) > 0 {
			if !validHash(string(id)) {
				return invalid
			}
			active = append(active, string(id))
		}
	}
	ids := []string{}
	unified, err := l.RecoveryRecord()
	if err != nil {
		return err
	}
	if unified != nil {
		if (len(wrapped) > 0) != (len(unified.WrappedVault) > 0) {
			return invalid
		}
		ids = append(ids, unified.ID)
	}
	rows, err := l.db.Query("SELECT key,value FROM settings WHERE key LIKE 'recovery-key-%' ORDER BY key")
	if err != nil {
		return err
	}
	defer rows.Close()
	records := map[string]bool{}
	for rows.Next() {
		var name string
		var data []byte
		if err = rows.Scan(&name, &data); err != nil {
			return err
		}
		id := strings.TrimPrefix(name, "recovery-key-")
		if id == "active-local" || id == "active-cloud" {
			continue
		}
		if !validHash(id) || !IDPattern.Match(libraryID) {
			return invalid
		}
		var record struct {
			Info struct {
				ID           string     `json:"id"`
				VaultPresent bool       `json:"vaultPresent"`
				RepositoryID string     `json:"repositoryID"`
				State        string     `json:"state"`
				CreatedAt    time.Time  `json:"createdAt"`
				VerifiedAt   *time.Time `json:"verifiedAt"`
				Snapshot     string     `json:"snapshot"`
			} `json:"info"`
			WrappedVault []byte `json:"wrappedVault"`
			VaultDigest  string `json:"vaultDigest"`
		}
		if json.Unmarshal(data, &record) != nil || record.Info.ID != id || !validHash(record.Info.RepositoryID) || !validHash(record.VaultDigest) {
			return invalid
		}
		if record.Info.CreatedAt.IsZero() || (record.Info.State != "exported" && record.Info.State != "verified") || record.Info.VaultPresent != (len(record.WrappedVault) > 0) {
			return invalid
		}
		if record.Info.State == "verified" && (record.Info.VerifiedAt == nil || record.Info.VerifiedAt.IsZero() || !validHash(record.Info.Snapshot)) {
			return invalid
		}
		records[id] = true
		// A key created before a private vault existed is valid but does not
		// cover the new vault. Preserve its record without advertising support.
		if record.Info.VaultPresent != (len(wrapped) > 0) {
			continue
		}
		ids = append(ids, id)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	for _, id := range active {
		if !records[id] {
			return invalid
		}
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
