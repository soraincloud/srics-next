package recoverykey

import (
	"bytes"
	"encoding/json"
	"filippo.io/age"
	"testing"
)

func TestEnvelopeAuthenticatesLibraryAndCiphertext(t *testing.T) {
	key, _ := age.GenerateX25519Identity()
	other, _ := age.GenerateX25519Identity()
	e := Envelope{LibraryID: "12345678901234567890123456789012", RepositoryID: Fingerprint("repo"), Snapshot: Fingerprint("snapshot"), Password: "synthetic-unique-password"}
	plain, _ := json.Marshal(e)
	cipher, err := Seal(key.Recipient().String(), plain)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(cipher, []byte(e.Password)) {
		t.Fatal("secret in ciphertext")
	}
	opened, err := OpenEnvelope(key.String(), cipher, e.LibraryID)
	if err != nil || opened != e {
		t.Fatal(err)
	}
	if _, err = OpenEnvelope(other.String(), cipher, e.LibraryID); err == nil {
		t.Fatal("wrong private key")
	}
	if _, err = OpenEnvelope(key.String(), cipher, "other-library"); err == nil {
		t.Fatal("wrong library")
	}
	cipher[len(cipher)-1] ^= 1
	if _, err = OpenEnvelope(key.String(), cipher, e.LibraryID); err == nil {
		t.Fatal("damaged envelope")
	}
}
