// Package webhookx sends and receives webhooks in the Standard Webhooks format
// (https://www.standardwebhooks.com): the webhook-id, webhook-timestamp and
// webhook-signature headers, an HMAC-SHA256 of "id.timestamp.body" and secrets written
// as "whsec_" followed by base64. Receivers on the other side verify it with any
// Standard Webhooks library.
//
// Sending is a River job inserted in the transaction of the change it reports (Sender),
// so a webhook goes out exactly when the change commits and is retried with backoff.
// Receiving is an http.Handler (Receiver) that verifies the signature, rejects stale
// timestamps and handles every webhook id once, in a transaction of its own.
package webhookx

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Headers of the Standard Webhooks format.
const (
	HeaderID        = "webhook-id"
	HeaderTimestamp = "webhook-timestamp"
	HeaderSignature = "webhook-signature"
)

// DefaultTolerance is how far a webhook timestamp may be from the receiver's clock.
const DefaultTolerance = 5 * time.Minute

const secretPrefix = "whsec_"

// Errors of Verify.
var (
	ErrMissingHeaders = errors.New("webhookx: missing webhook headers")
	ErrBadTimestamp   = errors.New("webhookx: invalid webhook timestamp")
	ErrStale          = errors.New("webhookx: webhook timestamp outside the tolerance")
	ErrBadSignature   = errors.New("webhookx: no matching signature")
)

// Secret is the key both sides sign with.
type Secret []byte

// ParseSecret reads a secret in the "whsec_<base64>" form; the prefix is optional.
func ParseSecret(s string) (Secret, error) {
	key, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(strings.TrimSpace(s), secretPrefix))
	if err != nil {
		return nil, fmt.Errorf("webhookx: secret: %w", err)
	}
	if len(key) < 24 {
		return nil, fmt.Errorf("webhookx: secret: %d bytes, want at least 24", len(key))
	}
	return key, nil
}

// NewSecret returns a random secret in the "whsec_<base64>" form, to hand to the other side.
func NewSecret() string {
	key := make([]byte, 32)
	_, _ = rand.Read(key) // crypto/rand never fails
	return secretPrefix + base64.StdEncoding.EncodeToString(key)
}

// String writes the secret in the "whsec_<base64>" form.
func (s Secret) String() string { return secretPrefix + base64.StdEncoding.EncodeToString(s) }

// Sign returns the webhook-signature value of a message.
func Sign(secret Secret, id string, at time.Time, body []byte) string {
	return "v1," + base64.StdEncoding.EncodeToString(mac(secret, id, at.Unix(), body))
}

func mac(secret Secret, id string, unix int64, body []byte) []byte {
	h := hmac.New(sha256.New, secret)
	h.Write([]byte(id))
	h.Write([]byte{'.'})
	h.Write([]byte(strconv.FormatInt(unix, 10)))
	h.Write([]byte{'.'})
	h.Write(body)
	return h.Sum(nil)
}

// SetHeaders signs body with secret and sets the three webhook headers on h.
func SetHeaders(h http.Header, secret Secret, id string, at time.Time, body []byte) {
	h.Set(HeaderID, id)
	h.Set(HeaderTimestamp, strconv.FormatInt(at.Unix(), 10))
	h.Set(HeaderSignature, Sign(secret, id, at, body))
}

// Verify checks the webhook headers of body against any of secrets — several while a
// secret is rotated — and the timestamp against now within tolerance. It returns the
// webhook id and timestamp.
func Verify(h http.Header, body []byte, secrets []Secret, now time.Time, tolerance time.Duration) (string, time.Time, error) {
	id, rawTS, sigs := h.Get(HeaderID), h.Get(HeaderTimestamp), h.Get(HeaderSignature)
	if id == "" || rawTS == "" || sigs == "" {
		return "", time.Time{}, ErrMissingHeaders
	}
	unix, err := strconv.ParseInt(rawTS, 10, 64)
	if err != nil {
		return "", time.Time{}, ErrBadTimestamp
	}
	at := time.Unix(unix, 0)
	if d := now.Sub(at); d > tolerance || d < -tolerance {
		return "", time.Time{}, ErrStale
	}
	for _, secret := range secrets {
		want := mac(secret, id, unix, body)
		for sig := range strings.FieldsSeq(sigs) {
			version, value, ok := strings.Cut(sig, ",")
			if !ok || version != "v1" {
				continue
			}
			got, err := base64.StdEncoding.DecodeString(value)
			if err == nil && hmac.Equal(got, want) {
				return id, at, nil
			}
		}
	}
	return "", time.Time{}, ErrBadSignature
}

// newID returns a message id in the msg_ form of Standard Webhooks senders.
func newID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return "msg_" + base64.RawURLEncoding.EncodeToString(b)
}
