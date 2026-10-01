package cryptox_test

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/aidarbn/platform-go/kit/cryptox"
)

func key(b byte) []byte { return bytes.Repeat([]byte{b}, cryptox.KeySize) }

func sealer(t *testing.T, b byte) *cryptox.Sealer {
	t.Helper()
	s, err := cryptox.NewSealer(key(b))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSealRoundTrip(t *testing.T) {
	s := sealer(t, 1)
	a, _ := s.Seal([]byte("+77011234567"))
	b, _ := s.Seal([]byte("+77011234567"))
	if bytes.Equal(a, b) {
		t.Error("equal ciphertexts: the nonce is not random")
	}
	plain, err := s.Open(a)
	if err != nil || string(plain) != "+77011234567" {
		t.Fatalf("Open = %q, %v", plain, err)
	}
	a[len(a)-1] ^= 1
	if _, err := s.Open(a); !errors.Is(err, cryptox.ErrOpen) {
		t.Errorf("a damaged value must not open, got %v", err)
	}
	if _, err := sealer(t, 9).Open(b); !errors.Is(err, cryptox.ErrOpen) {
		t.Errorf("another key must not open, got %v", err)
	}
	if _, err := s.Open([]byte{1, 2, 3}); !errors.Is(err, cryptox.ErrOpen) {
		t.Errorf("a short value must not open, got %v", err)
	}
}

// Values sealed and hashed before the code moved into the platform must keep working:
// the vectors come from the first project that stored them.
func TestStoredFormat(t *testing.T) {
	sealed, _ := hex.DecodeString("018e2ed2e0cbd9bad3f6f6f0415d16fe3395e3cadf41e454ad8b6f6242bed8ac950506e749984df632")
	plain, err := sealer(t, 1).Open(sealed)
	if err != nil || string(plain) != "+77011234567" {
		t.Fatalf("Open = %q, %v", plain, err)
	}

	index, _ := cryptox.NewMAC(key(2))
	idx := index.Sum([]byte("+77011234567"))
	if got := hex.EncodeToString(idx); got != "03308ac65ee93ea79202678674ac265fd5319517315201b6e483ba5fbe86de3e" {
		t.Errorf("blind index = %s", got)
	}
	codes, _ := cryptox.NewMAC(key(3))
	if got := hex.EncodeToString(codes.Sum(idx, []byte("123456"))); got != "e82f4661b551598a1a8a8f7b311fe119941fcc1d7695165978e9e34de7463b07" {
		t.Errorf("code hash = %s", got)
	}
}

func TestMAC(t *testing.T) {
	a, _ := cryptox.NewMAC(key(2))
	b, _ := cryptox.NewMAC(key(9))
	if !bytes.Equal(a.Sum([]byte("x")), a.Sum([]byte("x"))) {
		t.Error("the MAC is not deterministic")
	}
	if bytes.Equal(a.Sum([]byte("x")), b.Sum([]byte("x"))) {
		t.Error("the MAC does not depend on the key")
	}
	if !bytes.Equal(a.Sum([]byte("ab"), []byte("c")), a.Sum([]byte("abc"))) {
		t.Error("parts are written one after another")
	}
}

func TestTokens(t *testing.T) {
	token, hash, err := cryptox.NewToken()
	if err != nil || len(token) != 43 || !cryptox.Equal(hash, cryptox.HashToken(token)) {
		t.Fatalf("NewToken = %q, %x, %v", token, hash, err)
	}
	other, _, _ := cryptox.NewToken()
	if other == token {
		t.Error("tokens repeat")
	}
}

func TestKeys(t *testing.T) {
	raw, err := cryptox.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	if k, err := cryptox.ParseKey(raw); err != nil || len(k) != cryptox.KeySize {
		t.Errorf("NewKey must give what ParseKey reads: %v", err)
	}
	if k, err := cryptox.ParseKey(base64.RawURLEncoding.EncodeToString(key(5))); err != nil || !bytes.Equal(k, key(5)) {
		t.Errorf("URL base64 without padding is a key too: %v", err)
	}
	if _, err := cryptox.ParseKey(base64.StdEncoding.EncodeToString(make([]byte, 16))); err == nil {
		t.Error("a short key is accepted")
	}
	if _, err := cryptox.ParseKey("not base64!"); err == nil {
		t.Error("garbage is accepted")
	}
	if _, err := cryptox.NewSealer(key(1)[:16]); err == nil {
		t.Error("a short encryption key is accepted")
	}
	if _, err := cryptox.NewMAC(nil); err == nil {
		t.Error("an empty MAC key is accepted")
	}
}
