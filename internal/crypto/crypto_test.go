package crypto

import (
	"bytes"
	"strings"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	plain := []byte(`{"type":"user","cwd":"/home/alice/project"}` + "\nsecond line with unicode: xin chào\n")
	blob, err := Encrypt("correct horse", plain)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(blob, []byte("alice")) || bytes.Contains(blob, []byte("cwd")) {
		t.Fatal("ciphertext leaks plaintext")
	}
	got, err := Decrypt("correct horse", blob)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatal("round-trip mismatch")
	}
}

func TestWrongPassphraseFails(t *testing.T) {
	blob, err := Encrypt("right", []byte("data"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Decrypt("wrong", blob); err == nil {
		t.Fatal("decrypt with wrong passphrase should fail")
	}
}

func TestCheckBlob(t *testing.T) {
	blob, err := NewCheckBlob("pass")
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyCheckBlob("pass", blob); err != nil {
		t.Fatalf("valid passphrase rejected: %v", err)
	}
	err = VerifyCheckBlob("nope", blob)
	if err == nil {
		t.Fatal("wrong passphrase accepted")
	}
	if !strings.Contains(err.Error(), "passphrase") {
		t.Fatalf("error should hint at the passphrase, got: %v", err)
	}
}
