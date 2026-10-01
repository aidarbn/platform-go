// Package cryptox protects personal data at rest: field encryption, blind indexes to
// search encrypted values, and random tokens stored as hashes. It is pure — keys come
// from the project, nothing touches the database or the network.
//
// Every purpose takes its own key, so a leaked index key reveals no ciphertext and the
// other way round. Keys are 32 random bytes in base64: openssl rand -base64 32.
package cryptox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
)

// KeySize is the size of every key.
const KeySize = 32

// ParseKey decodes a key in standard or URL base64, padded or not, and checks its size.
func ParseKey(raw string) ([]byte, error) {
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if key, err := enc.DecodeString(raw); err == nil {
			if len(key) != KeySize {
				return nil, fmt.Errorf("the key is %d bytes, %d are needed: openssl rand -base64 32", len(key), KeySize)
			}
			return key, nil
		}
	}
	return nil, errors.New("the key is not base64: generate one with openssl rand -base64 32")
}

// NewKey returns a random key in standard base64, the form ParseKey reads.
func NewKey() (string, error) {
	key := make([]byte, KeySize)
	if _, err := rand.Read(key); err != nil {
		return "", fmt.Errorf("cryptox: key: %w", err)
	}
	return base64.StdEncoding.EncodeToString(key), nil
}

// version is the first byte of a sealed value. It leaves room to change the key or the
// cipher without re-encrypting everything at once.
const version byte = 1

// ErrOpen means the value is damaged or was sealed with another key.
var ErrOpen = errors.New("cryptox: cannot open the sealed value")

// Sealer encrypts values with AES-256-GCM: version, nonce, ciphertext with its tag.
type Sealer struct{ aead cipher.AEAD }

// NewSealer checks the key.
func NewSealer(key []byte) (*Sealer, error) {
	if len(key) != KeySize {
		return nil, fmt.Errorf("cryptox: the encryption key must be %d bytes", KeySize)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("cryptox: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("cryptox: %w", err)
	}
	return &Sealer{aead: aead}, nil
}

// Seal encrypts plain. Sealing the same value twice gives different results: search by
// value goes through a blind index.
func (s *Sealer) Seal(plain []byte) ([]byte, error) {
	nonce := make([]byte, s.aead.NonceSize(), 1+s.aead.NonceSize()+len(plain)+s.aead.Overhead())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("cryptox: nonce: %w", err)
	}
	out := append([]byte{version}, nonce...)
	return s.aead.Seal(out, nonce, plain, []byte{version}), nil
}

// Open decrypts what Seal produced.
func (s *Sealer) Open(sealed []byte) ([]byte, error) {
	n := s.aead.NonceSize()
	if len(sealed) < 1+n+s.aead.Overhead() || sealed[0] != version {
		return nil, ErrOpen
	}
	plain, err := s.aead.Open(nil, sealed[1:1+n], sealed[1+n:], []byte{version})
	if err != nil {
		return nil, ErrOpen
	}
	return plain, nil
}

// MAC is HMAC-SHA256 under a server key. As a blind index it finds a record by a value
// stored only encrypted; as a hash of a short secret, such as a six digit code, it cannot
// be brute forced without the key, unlike a plain hash.
type MAC struct{ key []byte }

// NewMAC checks the key.
func NewMAC(key []byte) (*MAC, error) {
	if len(key) != KeySize {
		return nil, fmt.Errorf("cryptox: the MAC key must be %d bytes", KeySize)
	}
	return &MAC{key: key}, nil
}

// Sum returns the MAC of the parts written one after another. The parts are not
// delimited: when more than one varies in length, make them fixed size first.
func (m *MAC) Sum(parts ...[]byte) []byte {
	h := hmac.New(sha256.New, m.key)
	for _, p := range parts {
		h.Write(p)
	}
	return h.Sum(nil)
}

// NewToken returns 32 random bytes in URL base64 to hand out and their hash to store.
func NewToken() (token string, hash []byte, err error) {
	raw := make([]byte, KeySize)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, fmt.Errorf("cryptox: token: %w", err)
	}
	token = base64.RawURLEncoding.EncodeToString(raw)
	return token, HashToken(token), nil
}

// HashToken is SHA-256 of a token. A token is long and random, so it needs no salt.
func HashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

// Equal compares hashes in constant time.
func Equal(a, b []byte) bool { return hmac.Equal(a, b) }
