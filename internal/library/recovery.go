package library

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/soraincloud/srics-next/internal/recoverykey"
	"github.com/soraincloud/srics-next/internal/vault"
)

func (l *Library) RecoveryRecord() (*recoverykey.Record, error) {
	b, err := l.Setting(recoverykey.Setting)
	if err != nil {
		return nil, err
	}
	r, err := recoverykey.Decode(b)
	if err != nil || r == nil {
		return r, err
	}
	id, err := l.Setting("recovery-library-id")
	if err != nil || string(id) != r.LibraryID {
		return nil, errors.New("恢复钥匙与资料库不匹配")
	}
	return r, nil
}

// Publish this together with vault-key in a single credentials transaction.
func (l *Library) VaultRecoverySettings(wrapped []byte, password string) (map[string][]byte, error) {
	r, err := l.RecoveryRecord()
	if err != nil || r == nil {
		return nil, err
	}
	id, err := vault.Unlock(wrapped, password)
	if err != nil {
		return nil, err
	}
	plain := []byte(id.String())
	defer clear(plain)
	r.WrappedVault, err = recoverykey.Seal(r.Recipient, plain)
	if err != nil {
		return nil, err
	}
	r.WrappedVaultHash = recoverykey.Fingerprint(string(r.WrappedVault))
	b, err := json.Marshal(r)
	return map[string][]byte{recoverykey.Setting: b}, err
}

func (l *Library) RootPath() string { return l.Root }
func (l *Library) VerifySnapshot(ctx context.Context, path string) error {
	pinned, err := Open(path)
	if err != nil {
		return err
	}
	err = pinned.Verify(ctx)
	closeErr := pinned.Close()
	if err != nil {
		return err
	}
	return closeErr
}
