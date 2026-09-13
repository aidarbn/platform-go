package adminx

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// TOTP parameters. They are what authenticator apps assume by default, so a secret
// scanned as a QR code works without extra settings.
const (
	totpStep    = 30 * time.Second
	totpDigits  = 6
	totpSkew    = 1 // how many steps in each direction are accepted
	totpSecretN = 20
)

var (
	// ErrInvalidCode is returned when the code does not match the secret.
	ErrInvalidCode = errors.New("invalid one time code")

	// ErrCodeReused is returned when a code has already been used. A one time code that
	// works twice is not one time: an intercepted code would be enough to log in.
	ErrCodeReused = errors.New("the code has already been used")
)

var totpEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewTOTPSecret returns a fresh secret in the base32 form an authenticator app expects.
func NewTOTPSecret() (string, error) {
	raw := make([]byte, totpSecretN)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("adminx: totp secret: %w", err)
	}
	return totpEncoding.EncodeToString(raw), nil
}

// TOTPCode returns the code for a moment in time.
func TOTPCode(secret string, at time.Time) (string, error) {
	return totpAt(secret, at.UTC().Unix()/int64(totpStep.Seconds()))
}

// VerifyTOTP checks a code and returns the counter it was issued for. A code from an
// earlier or the same counter as lastCounter is refused, which is what stops a code
// from being used twice; store the returned counter next to the user.
func VerifyTOTP(secret, code string, now time.Time, lastCounter int64) (int64, error) {
	code = strings.TrimSpace(code)
	if len(code) != totpDigits {
		return 0, ErrInvalidCode
	}

	current := now.UTC().Unix() / int64(totpStep.Seconds())
	for counter := current - totpSkew; counter <= current+totpSkew; counter++ {
		want, err := totpAt(secret, counter)
		if err != nil {
			return 0, err
		}
		if subtle.ConstantTimeCompare([]byte(want), []byte(code)) != 1 {
			continue
		}
		if counter <= lastCounter {
			return 0, ErrCodeReused
		}
		return counter, nil
	}
	return 0, ErrInvalidCode
}

// TOTPURI returns the otpauth link an authenticator app reads from a QR code.
func TOTPURI(issuer, account, secret string) string {
	label := url.PathEscape(issuer + ":" + account)
	q := url.Values{
		"secret": {secret},
		"issuer": {issuer},
		"digits": {fmt.Sprint(totpDigits)},
		"period": {fmt.Sprint(int(totpStep.Seconds()))},
	}
	return "otpauth://totp/" + label + "?" + q.Encode()
}

func totpAt(secret string, counter int64) (string, error) {
	key, err := totpEncoding.DecodeString(strings.ToUpper(strings.ReplaceAll(secret, " ", "")))
	if err != nil {
		return "", fmt.Errorf("adminx: totp secret: %w", err)
	}
	if len(key) == 0 {
		return "", errors.New("adminx: empty totp secret")
	}

	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(counter))

	mac := hmac.New(sha1.New, key)
	mac.Write(msg[:])
	sum := mac.Sum(nil)

	// Dynamic truncation, RFC 4226 section 5.3.
	offset := sum[len(sum)-1] & 0x0f
	value := binary.BigEndian.Uint32(sum[offset:offset+4]) & 0x7fffffff

	mod := uint32(1)
	for range totpDigits {
		mod *= 10
	}
	return fmt.Sprintf("%0*d", totpDigits, value%mod), nil
}
