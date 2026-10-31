// Package auth implements authentication to the LightMail web interface
// (local users, sessions, brute-force protection). It is unrelated to the
// OAuth2 authentication against mail providers (see package oauth).
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"runtime/debug"
	"strings"
	"sync"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

// Argon2id parameters: OWASP recommended alternative m=19 MiB, t=2, p=1
// (same security level as m=46 MiB, t=1, lighter on small VPS). Hashing is
// limited to two concurrent operations and memory is returned to the OS
// right after, so logins do not inflate the resident footprint.
const (
	argonTime    = 2
	argonMemory  = 19 * 1024
	argonThreads = 1
	argonKeyLen  = 32
	saltLen      = 16

	MinPasswordLength = 10
)

var hashSem = make(chan struct{}, 2)

var ErrWeakPassword = fmt.Errorf("password must contain at least %d characters", MinPasswordLength)

// HashPassword returns a PHC-formatted Argon2id hash.
func HashPassword(password string) (string, error) {
	if err := CheckPasswordPolicy(password); err != nil {
		return "", err
	}
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := hashWithLimit(func() []byte {
		return argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	})
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

func CheckPasswordPolicy(password string) error {
	if utf8.RuneCountInString(password) < MinPasswordLength {
		return ErrWeakPassword
	}
	if len(password) > 1024 {
		return errors.New("password too long")
	}
	return nil
}

// VerifyPassword checks password against a PHC Argon2id hash.
func VerifyPassword(encoded, password string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false
	}
	var m uint32
	var t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil || m > 1<<20 || t > 10 || p == 0 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(want) == 0 {
		return false
	}
	if len(password) > 1024 {
		return false
	}
	got := hashWithLimit(func() []byte { return argon2.IDKey([]byte(password), salt, t, m, p, uint32(len(want))) })
	return subtle.ConstantTimeCompare(got, want) == 1
}

func hashWithLimit(f func() []byte) []byte {
	hashSem <- struct{}{}
	defer func() {
		<-hashSem
		debug.FreeOSMemory()
	}()
	return f()
}

// dummyHash is verified when the user does not exist, so that response
// time does not reveal valid usernames. Computed lazily.
var dummyHash = sync.OnceValue(func() string {
	h, _ := HashPassword("lightmail-dummy-password")
	return h
})
