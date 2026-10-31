// Package crypto provides authenticated encryption of secrets at rest.
//
// Ciphertext format: "v1." + keyID + "." + base64url(nonce || sealed).
// keyID is the first 8 hex chars of SHA-256(key) so the right key can be
// selected after a rotation. Each value is bound to its context (table,
// row and field) through AES-GCM additional data, so a ciphertext copied to
// another row or field fails to decrypt.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

var (
	ErrUnknownKey = errors.New("crypto: ciphertext was encrypted with an unknown key (missing LIGHTMAIL_MASTER_KEY_OLD?)")
	ErrDecrypt    = errors.New("crypto: decryption failed (wrong key or tampered data)")
	ErrFormat     = errors.New("crypto: invalid ciphertext format")
)

type key struct {
	id   string
	aead cipher.AEAD
}

// Keyring encrypts with the primary key and decrypts with any known key.
type Keyring struct {
	primary key
	all     map[string]key
}

func NewKeyring(primary []byte, old ...[]byte) (*Keyring, error) {
	p, err := newKey(primary)
	if err != nil {
		return nil, err
	}
	kr := &Keyring{primary: p, all: map[string]key{p.id: p}}
	for _, o := range old {
		k, err := newKey(o)
		if err != nil {
			return nil, err
		}
		kr.all[k.id] = k
	}
	return kr, nil
}

func newKey(raw []byte) (key, error) {
	if len(raw) != 32 {
		return key{}, errors.New("crypto: key must be 32 bytes")
	}
	block, err := aes.NewCipher(raw)
	if err != nil {
		return key{}, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return key{}, err
	}
	return key{id: KeyID(raw), aead: aead}, nil
}

// KeyID returns the public fingerprint of a key.
func KeyID(raw []byte) string {
	sum := sha256.Sum256(append([]byte("lightmail-key-id:"), raw...))
	return hex.EncodeToString(sum[:4])
}

// PrimaryID returns the fingerprint of the current encryption key.
func (k *Keyring) PrimaryID() string { return k.primary.id }

// Encrypt seals plaintext bound to context. Empty plaintext yields "".
func (k *Keyring) Encrypt(plaintext, context string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	nonce := make([]byte, k.primary.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := k.primary.aead.Seal(nonce, nonce, []byte(plaintext), []byte(context))
	return "v1." + k.primary.id + "." + base64.RawURLEncoding.EncodeToString(sealed), nil
}

// Decrypt opens a value produced by Encrypt with the same context.
func (k *Keyring) Decrypt(ciphertext, context string) (string, error) {
	if ciphertext == "" {
		return "", nil
	}
	parts := strings.SplitN(ciphertext, ".", 3)
	if len(parts) != 3 || parts[0] != "v1" {
		return "", ErrFormat
	}
	kk, ok := k.all[parts[1]]
	if !ok {
		return "", ErrUnknownKey
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(raw) < kk.aead.NonceSize()+kk.aead.Overhead() {
		return "", ErrFormat
	}
	ns := kk.aead.NonceSize()
	pt, err := kk.aead.Open(nil, raw[:ns], raw[ns:], []byte(context))
	if err != nil {
		return "", ErrDecrypt
	}
	return string(pt), nil
}

// NeedsRotation reports whether a ciphertext uses a non-primary key.
func (k *Keyring) NeedsRotation(ciphertext string) bool {
	if ciphertext == "" {
		return false
	}
	parts := strings.SplitN(ciphertext, ".", 3)
	return len(parts) != 3 || parts[1] != k.primary.id
}

// RandomToken returns n random bytes encoded as base64url.
func RandomToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("crypto/rand failure: %v", err))
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// HashToken returns the hex SHA-256 of a token, used to store session and
// state identifiers without keeping the bearer value.
func HashToken(t string) string {
	s := sha256.Sum256([]byte(t))
	return hex.EncodeToString(s[:])
}

// ConstantTimeEqual compares two strings in constant time.
func ConstantTimeEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
