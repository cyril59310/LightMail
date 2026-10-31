package crypto

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func k(b byte) []byte { return bytes.Repeat([]byte{b}, 32) }

func TestRoundTrip(t *testing.T) {
	kr, err := NewKeyring(k(1))
	if err != nil {
		t.Fatal(err)
	}
	ct, err := kr.Encrypt("refresh-token-value", "account:1:refresh")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(ct, "refresh-token-value") {
		t.Fatal("plaintext visible in ciphertext")
	}
	pt, err := kr.Decrypt(ct, "account:1:refresh")
	if err != nil || pt != "refresh-token-value" {
		t.Fatalf("got %q %v", pt, err)
	}
	ct2, _ := kr.Encrypt("refresh-token-value", "account:1:refresh")
	if ct == ct2 {
		t.Fatal("nonce reuse: identical ciphertexts")
	}
}

func TestContextBinding(t *testing.T) {
	kr, _ := NewKeyring(k(1))
	ct, _ := kr.Encrypt("secret", "account:1:password")
	if _, err := kr.Decrypt(ct, "account:2:password"); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("expected decrypt failure, got %v", err)
	}
}

func TestTamper(t *testing.T) {
	kr, _ := NewKeyring(k(1))
	ct, _ := kr.Encrypt("secret", "c")
	b := []byte(ct)
	b[len(b)-2] ^= 1
	if _, err := kr.Decrypt(string(b), "c"); err == nil {
		t.Fatal("tampered ciphertext accepted")
	}
}

func TestRotation(t *testing.T) {
	old, _ := NewKeyring(k(1))
	ct, _ := old.Encrypt("secret", "c")

	wrong, _ := NewKeyring(k(2))
	if _, err := wrong.Decrypt(ct, "c"); !errors.Is(err, ErrUnknownKey) {
		t.Fatalf("expected unknown key, got %v", err)
	}

	rotated, _ := NewKeyring(k(2), k(1))
	if !rotated.NeedsRotation(ct) {
		t.Fatal("expected rotation needed")
	}
	pt, err := rotated.Decrypt(ct, "c")
	if err != nil || pt != "secret" {
		t.Fatalf("got %q %v", pt, err)
	}
	ct2, _ := rotated.Encrypt(pt, "c")
	if rotated.NeedsRotation(ct2) {
		t.Fatal("new ciphertext should use primary key")
	}
}

func TestEmpty(t *testing.T) {
	kr, _ := NewKeyring(k(1))
	ct, _ := kr.Encrypt("", "c")
	pt, err := kr.Decrypt(ct, "c")
	if ct != "" || pt != "" || err != nil {
		t.Fatal("empty handling")
	}
}
