// Package vault uses age's authenticated file format. Private keys are never
// written unwrapped. This package is a storage primitive, not a session manager.
package vault

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"

	"filippo.io/age"
)

var ErrLocked = errors.New("vault is locked")

func Create(passphrase string) (*age.X25519Identity, []byte, error) {
	if len(passphrase) < 12 {
		return nil, nil, errors.New("vault passphrase must contain at least 12 bytes")
	}
	id, err := age.GenerateX25519Identity()
	if err != nil {
		return nil, nil, err
	}
	recipient, err := age.NewScryptRecipient(passphrase)
	if err != nil {
		return nil, nil, err
	}
	var buf bytes.Buffer
	if err = Encrypt(&buf, bytes.NewBufferString(id.String()), recipient); err != nil {
		return nil, nil, err
	}
	return id, buf.Bytes(), nil
}

func Unlock(wrapped []byte, passphrase string) (*age.X25519Identity, error) {
	id, err := age.NewScryptIdentity(passphrase)
	if err != nil {
		return nil, errors.New("cannot unlock vault")
	}
	id.SetMaxWorkFactor(18)
	plain, err := Decrypt(wrapped, id, 4096)
	if err != nil {
		return nil, errors.New("cannot unlock vault")
	}
	defer clear(plain)
	key, err := age.ParseX25519Identity(string(plain))
	if err != nil {
		return nil, errors.New("invalid wrapped identity")
	}
	return key, nil
}

func Encrypt(dst io.Writer, src io.Reader, recipient age.Recipient) error {
	if recipient == nil {
		return ErrLocked
	}
	w, err := age.Encrypt(dst, recipient)
	if err != nil {
		return err
	}
	if _, err = io.Copy(w, src); err != nil {
		return err
	}
	return w.Close()
}

// Decrypt withholds the result until authentication, including the final chunk,
// succeeds. This bounded helper is for metadata and small verification fixtures.
func Decrypt(ciphertext []byte, identity age.Identity, limit int64) ([]byte, error) {
	if identity == nil {
		return nil, ErrLocked
	}
	r, err := age.Decrypt(bytes.NewReader(ciphertext), identity)
	if err != nil {
		return nil, err
	}
	plain, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil || int64(len(plain)) > limit {
		clear(plain)
		return nil, fmt.Errorf("invalid ciphertext or plaintext exceeds limit")
	}
	return plain, nil
}

// Wrap changes the passphrase without re-encrypting immutable content. Historical
// snapshots keep their original wrapper and therefore their original passphrase.
func Wrap(key *age.X25519Identity, passphrase string) ([]byte, error) {
	if len(passphrase) < 12 || len(passphrase) > 1024 {
		return nil, errors.New("保险库口令需为 12–1024 字节")
	}
	recipient, err := age.NewScryptRecipient(passphrase)
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	err = Encrypt(&b, strings.NewReader(key.String()), recipient)
	return b.Bytes(), err
}
