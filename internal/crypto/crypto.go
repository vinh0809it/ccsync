// Package crypto holds ccsync's encryption.
//
// A store has one random age X25519 key. The key itself is stored in the
// repo encrypted with the passphrase (scrypt), so unlocking costs one slow
// scrypt derivation; every session blob is then encrypted to the key, which
// is fast. Store format 1 encrypted every blob with scrypt directly; the
// passphrase functions remain for that format's check file and migration.
package crypto

import (
	"bytes"
	"fmt"
	"io"
	"strings"

	"filippo.io/age"
)

// checkPlaintext is a known value encrypted into check.age at store init,
// used to verify the passphrase on other machines before doing anything.
const checkPlaintext = "ccsync-check-v1"

// Key is a store's unlocked encryption key.
type Key struct {
	id *age.X25519Identity
}

// NewKey generates a fresh store key.
func NewKey() (*Key, error) {
	id, err := age.GenerateX25519Identity()
	if err != nil {
		return nil, err
	}
	return &Key{id: id}, nil
}

// Wrap encrypts the key with the passphrase, for storing in the repo.
func (k *Key) Wrap(passphrase string) ([]byte, error) {
	return Encrypt(passphrase, []byte(k.id.String()))
}

// UnwrapKey decrypts a wrapped key with the passphrase.
func UnwrapKey(passphrase string, blob []byte) (*Key, error) {
	plain, err := Decrypt(passphrase, blob)
	if err != nil {
		return nil, err
	}
	id, err := age.ParseX25519Identity(strings.TrimSpace(string(plain)))
	if err != nil {
		return nil, fmt.Errorf("store key is corrupt: %w", err)
	}
	return &Key{id: id}, nil
}

// Encrypt encrypts plaintext to the key.
func (k *Key) Encrypt(plaintext []byte) ([]byte, error) {
	return encrypt(k.id.Recipient(), plaintext)
}

// Decrypt decrypts a blob encrypted to the key.
func (k *Key) Decrypt(ciphertext []byte) ([]byte, error) {
	r, err := age.Decrypt(bytes.NewReader(ciphertext), k.id)
	if err != nil {
		return nil, fmt.Errorf("decrypt failed: %w", err)
	}
	return io.ReadAll(r)
}

// Encrypt encrypts plaintext with the passphrase (scrypt; slow by design).
func Encrypt(passphrase string, plaintext []byte) ([]byte, error) {
	recipient, err := age.NewScryptRecipient(passphrase)
	if err != nil {
		return nil, err
	}
	return encrypt(recipient, plaintext)
}

// Decrypt decrypts a blob encrypted with the passphrase.
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

func encrypt(r age.Recipient, plaintext []byte) ([]byte, error) {
	var buf bytes.Buffer
	w, err := age.Encrypt(&buf, r)
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
