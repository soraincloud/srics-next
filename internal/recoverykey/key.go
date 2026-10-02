// Package recoverykey implements the library-wide, offline emergency identity.
// Only public recipients and authenticated ciphertext are stored in the library.
package recoverykey

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"filippo.io/age"
	"github.com/soraincloud/srics-next/internal/vault"
	"io"
	"path/filepath"
	"time"
)

const Setting = "recovery-unified"

type Record struct {
	ID               string     `json:"id"`
	LibraryID        string     `json:"libraryID"`
	Recipient        string     `json:"recipient"`
	CreatedAt        time.Time  `json:"createdAt"`
	State            string     `json:"state"`
	VerifiedAt       *time.Time `json:"verifiedAt,omitempty"`
	ExportPath       string     `json:"exportPath,omitempty"`
	WrappedVaultHash string     `json:"wrappedVaultHash,omitempty"`
	WrappedVault     []byte     `json:"wrappedVault,omitempty"`
}

func Fingerprint(recipient string) string {
	h := sha256.Sum256([]byte(recipient))
	return hex.EncodeToString(h[:])
}
func (r Record) Validate() error {
	_, err := age.ParseX25519Recipient(r.Recipient)
	id, e := hex.DecodeString(r.LibraryID)
	if err != nil || e != nil || len(id) != 16 || r.ID != Fingerprint(r.Recipient) || r.CreatedAt.IsZero() || (r.State != "exported" && r.State != "verified") || (len(r.WrappedVault) > 0 && r.WrappedVaultHash != Fingerprint(string(r.WrappedVault))) || (len(r.WrappedVault) == 0 && r.WrappedVaultHash != "") || (r.ExportPath != "" && (!filepath.IsAbs(r.ExportPath) || len(r.ExportPath) > 4096)) || len(r.WrappedVault) > 8192 || (r.State == "verified" && (r.VerifiedAt == nil || r.VerifiedAt.IsZero())) {
		return errors.New("资料库恢复钥匙记录损坏，请保留已有备份")
	}
	return nil
}
func Decode(b []byte) (*Record, error) {
	if len(b) == 0 {
		return nil, nil
	}
	var r Record
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if d.Decode(&r) != nil || d.Decode(&struct{}{}) != io.EOF {
		return nil, errors.New("资料库恢复钥匙记录损坏")
	}
	if err := r.Validate(); err != nil {
		return nil, err
	}
	return &r, nil
}
func Seal(recipient string, plain []byte) ([]byte, error) {
	r, err := age.ParseX25519Recipient(recipient)
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	err = vault.Encrypt(&b, bytes.NewReader(plain), r)
	return b.Bytes(), err
}
func Open(secret string, ciphertext []byte) ([]byte, error) {
	if len(ciphertext) > 16384 {
		return nil, errors.New("恢复封装过大")
	}
	id, err := age.ParseX25519Identity(secret)
	if err != nil {
		return nil, err
	}
	return vault.Decrypt(ciphertext, id, 8192)
}

type Envelope struct {
	LibraryID    string `json:"libraryID"`
	RepositoryID string `json:"repositoryID"`
	Snapshot     string `json:"snapshot"`
	Password     string `json:"password"`
}

func OpenEnvelope(secret string, ciphertext []byte, libraryID string) (Envelope, error) {
	var e Envelope
	b, err := Open(secret, ciphertext)
	if err != nil {
		return e, errors.New("恢复 JSON 无法解锁此备份")
	}
	defer clear(b)
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if d.Decode(&e) != nil || d.Decode(&struct{}{}) != io.EOF || e.LibraryID != libraryID || len(e.Password) < 12 || len(e.Password) > 1024 {
		return Envelope{}, errors.New("恢复封装无效")
	}
	for _, s := range []string{e.RepositoryID, e.Snapshot} {
		h, err := hex.DecodeString(s)
		if err != nil || len(h) != 32 {
			return Envelope{}, errors.New("恢复封装无效")
		}
	}
	return e, nil
}
