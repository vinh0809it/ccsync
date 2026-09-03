// Package crypto wraps age passphrase encryption (scrypt) so that every
// object stored in the sync repo is unreadable without the passphrase.
package crypto

import (
	"bytes"
	"fmt"
	"io"

	"filippo.io/age"
)

// checkPlaintext is a known value encrypted into check.age at store init,
// used to verify the passphrase on other machines before doing anything.
const checkPlaintext = "ccsync-check-v1"

func Encrypt(passphrase string, plaintext []byte) ([]byte, error) {
	recipient, err := age.NewScryptRecipient(passphrase)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	w, err := age.Encrypt(&buf, recipient)
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(plaintext); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func Decrypt(passphrase string, ciphertext []byte) ([]byte, error) {
	identity, err := age.NewScryptIdentity(passphrase)
	if err != nil {
		return nil, err
	}
	r, err := age.Decrypt(bytes.NewReader(ciphertext), identity)
	if err != nil {
		return nil, fmt.Errorf("decrypt failed (wrong passphrase?): %w", err)
	}
	return io.ReadAll(r)
}

func NewCheckBlob(passphrase string) ([]byte, error) {
	return Encrypt(passphrase, []byte(checkPlaintext))
}

func VerifyCheckBlob(passphrase string, blob []byte) error {
	got, err := Decrypt(passphrase, blob)
	if err != nil {
		return err
	}
	if string(got) != checkPlaintext {
		return fmt.Errorf("passphrase check file is corrupt")
	}
	return nil
}
