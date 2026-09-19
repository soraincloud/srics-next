package vault

import (
	"bytes"
	"testing"

	"filippo.io/age"
)

func TestCiphertextAuthentication(t *testing.T) {
	key, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	other, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	plain := bytes.Repeat([]byte("private fixture\x00"), 10000)
	var buf bytes.Buffer
	if err := Encrypt(&buf, bytes.NewReader(plain), key.Recipient()); err != nil {
		t.Fatal(err)
	}
	ciphertext := buf.Bytes()
	got, err := Decrypt(ciphertext, key, 1<<20)
	if err != nil || !bytes.Equal(got, plain) {
		t.Fatal("roundtrip", err)
	}
	tampered := bytes.Clone(ciphertext)
	tampered[len(tampered)-1] ^= 1
	for name, run := range map[string]func() ([]byte, error){
		"tamper":     func() ([]byte, error) { return Decrypt(tampered, key, 1<<20) },
		"truncated":  func() ([]byte, error) { return Decrypt(ciphertext[:len(ciphertext)-1], key, 1<<20) },
		"wrong key":  func() ([]byte, error) { return Decrypt(ciphertext, other, 1<<20) },
		"locked":     func() ([]byte, error) { return Decrypt(ciphertext, nil, 1<<20) },
		"size limit": func() ([]byte, error) { return Decrypt(ciphertext, key, 8) },
	} {
		t.Run(name, func(t *testing.T) {
			data, err := run()
			if err == nil || data != nil {
				t.Fatal("unauthenticated or oversized plaintext returned")
			}
		})
	}
}
func TestWrappedIdentity(t *testing.T) {
	key, wrapped, err := Create("test-only-vault-passphrase")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(wrapped, []byte(key.String())) {
		t.Fatal("raw private key persisted")
	}
	opened, err := Unlock(wrapped, "test-only-vault-passphrase")
	if err != nil || opened.String() != key.String() {
		t.Fatal("cannot recover identity", err)
	}
	if _, err = Unlock(wrapped, "different-test-passphrase"); err == nil {
		t.Fatal("wrong passphrase accepted")
	}
}
