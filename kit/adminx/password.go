// Package adminx holds the primitives the admin UI is built from: passwords, one time
// codes, sessions and the audit log.
//
// Everything here relies on the standard library only. An admin panel guards the
// production settings of a service, so the fewer moving parts under it, the better.
package adminx

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// ErrWrongPassword is returned when the password does not match the hash. It is the
// same error for a wrong password and for an unknown user, so the answer does not say
// which accounts exist.
var ErrWrongPassword = errors.New("wrong password")

const (
	// passwordIterations follows the OWASP recommendation for PBKDF2-HMAC-SHA256.
	// Verification reads the number from the stored hash, so raising it later keeps
	// the hashes already in the database working.
	passwordIterations = 600_000

	passwordSaltLen = 16
	passwordKeyLen  = 32
	passwordScheme  = "pbkdf2-sha256"
)

// HashPassword hashes a password for storage. The result keeps the scheme, the
// iteration count and the salt, so a hash stays verifiable after the parameters change.
func HashPassword(password string) (string, error) {
	if password == "" {
		return "", errors.New("an empty password")
	}

	salt := make([]byte, passwordSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("adminx: salt: %w", err)
	}
	key, err := pbkdf2.Key(sha256.New, password, salt, passwordIterations, passwordKeyLen)
	if err != nil {
		return "", fmt.Errorf("adminx: hash password: %w", err)
	}

	enc := base64.RawStdEncoding
	return strings.Join([]string{
		passwordScheme,
		strconv.Itoa(passwordIterations),
		enc.EncodeToString(salt),
		enc.EncodeToString(key),
	}, "$"), nil
}

// VerifyPassword checks a password against a stored hash.
func VerifyPassword(hash, password string) error {
	parts := strings.Split(hash, "$")
	if len(parts) != 4 || parts[0] != passwordScheme {
		return fmt.Errorf("adminx: unknown password hash format")
	}

	iterations, err := strconv.Atoi(parts[1])
	if err != nil || iterations <= 0 {
		return fmt.Errorf("adminx: password hash: bad iteration count %q", parts[1])
	}

	enc := base64.RawStdEncoding
	salt, err := enc.DecodeString(parts[2])
	if err != nil {
		return fmt.Errorf("adminx: password hash: bad salt")
	}
	want, err := enc.DecodeString(parts[3])
	if err != nil {
		return fmt.Errorf("adminx: password hash: bad key")
	}

	got, err := pbkdf2.Key(sha256.New, password, salt, iterations, len(want))
	if err != nil {
		return fmt.Errorf("adminx: hash password: %w", err)
	}
	if subtle.ConstantTimeCompare(got, want) != 1 {
		return ErrWrongPassword
	}
	return nil
}
