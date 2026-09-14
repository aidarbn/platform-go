package webx

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"time"
)

// Cookies keeps small server state in the browser, sealed with AES-GCM: which step of a
// form the visitor is on, which code is expected. The browser can neither read nor
// change it, and a cookie moved to another name does not open.
type Cookies struct {
	aead   cipher.AEAD
	secure bool
	now    func() time.Time
}

// NewCookies derives the cookie key from the secret of the web module.
func NewCookies(secret []byte, secure bool) (*Cookies, error) {
	if len(secret) < 32 {
		return nil, errors.New("webx: the secret must be at least 32 bytes")
	}
	key := sha256.Sum256(append([]byte("webx cookies\x00"), secret...))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Cookies{aead: aead, secure: secure, now: time.Now}, nil
}

type sealed struct {
	Value   json.RawMessage `json:"v"`
	Expires int64           `json:"e"`
}

// Set stores v under name for ttl.
func (c *Cookies) Set(w http.ResponseWriter, name string, v any, ttl time.Duration) error {
	value, err := json.Marshal(v)
	if err != nil {
		return err
	}
	plain, _ := json.Marshal(sealed{Value: value, Expires: c.now().Add(ttl).Unix()})
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	out := c.aead.Seal(nonce, nonce, plain, []byte(name))
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: base64.RawURLEncoding.EncodeToString(out), Path: "/", MaxAge: int(ttl.Seconds()),
		HttpOnly: true, Secure: c.secure, SameSite: http.SameSiteLaxMode,
	})
	return nil
}

// Get reads the cookie into v; false when it is missing, expired or tampered with.
func (c *Cookies) Get(r *http.Request, name string, v any) bool {
	cookie, err := r.Cookie(name)
	if err != nil {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(cookie.Value)
	n := c.aead.NonceSize()
	if err != nil || len(raw) < n {
		return false
	}
	plain, err := c.aead.Open(nil, raw[:n], raw[n:], []byte(name))
	if err != nil {
		return false
	}
	var s sealed
	if json.Unmarshal(plain, &s) != nil || c.now().Unix() > s.Expires {
		return false
	}
	return json.Unmarshal(s.Value, v) == nil
}

// Clear removes the cookie.
func (c *Cookies) Clear(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: c.secure, SameSite: http.SameSiteLaxMode})
}
