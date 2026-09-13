package adminx_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aidarbn/platform-go/kit/adminx"
)

func TestPasswordRoundTrip(t *testing.T) {
	hash, err := adminx.HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}

	if err := adminx.VerifyPassword(hash, "correct horse battery staple"); err != nil {
		t.Errorf("VerifyPassword: %v", err)
	}
	if err := adminx.VerifyPassword(hash, "correct horse battery stapl"); !errors.Is(err, adminx.ErrWrongPassword) {
		t.Errorf("a wrong password gave %v", err)
	}
	if !strings.HasPrefix(hash, "pbkdf2-sha256$600000$") {
		t.Errorf("hash = %q", hash)
	}
}

// The salt is random, so the same password hashes differently every time.
func TestPasswordHashesDiffer(t *testing.T) {
	first, err := adminx.HashPassword("secret")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	second, err := adminx.HashPassword("secret")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if first == second {
		t.Error("two hashes of one password are identical")
	}
	if err := adminx.VerifyPassword(second, "secret"); err != nil {
		t.Errorf("VerifyPassword: %v", err)
	}
}

func TestPasswordRejectsEmpty(t *testing.T) {
	if _, err := adminx.HashPassword(""); err == nil {
		t.Fatal("an empty password was accepted")
	}
}

// The iteration count is read from the hash, so hashes made with other parameters keep
// working after the recommendation changes.
func TestVerifyPasswordReadsStoredParameters(t *testing.T) {
	cases := map[string]string{
		"not a hash":      "hunter2",
		"unknown scheme":  "bcrypt$10$abc$def",
		"bad iterations":  "pbkdf2-sha256$many$YWJj$ZGVm",
		"zero iterations": "pbkdf2-sha256$0$YWJj$ZGVm",
		"bad base64 salt": "pbkdf2-sha256$1000$!!!$ZGVm",
		"bad base64 key":  "pbkdf2-sha256$1000$YWJj$!!!",
		"too few fields":  "pbkdf2-sha256$1000$YWJj",
	}
	for name, hash := range cases {
		t.Run(name, func(t *testing.T) {
			err := adminx.VerifyPassword(hash, "secret")
			if err == nil || errors.Is(err, adminx.ErrWrongPassword) {
				t.Fatalf("err = %v, want a format error", err)
			}
		})
	}
}

// RFC 6238 test vector: the secret "12345678901234567890" at 59 seconds.
func TestTOTPMatchesRFCVector(t *testing.T) {
	const secret = "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"

	code, err := adminx.TOTPCode(secret, time.Unix(59, 0))
	if err != nil {
		t.Fatalf("TOTPCode: %v", err)
	}
	if code != "287082" {
		t.Errorf("code = %q, want 287082", code)
	}

	code, err = adminx.TOTPCode(secret, time.Unix(1111111109, 0))
	if err != nil {
		t.Fatalf("TOTPCode: %v", err)
	}
	if code != "081804" {
		t.Errorf("code = %q, want 081804", code)
	}
}

func TestTOTPVerify(t *testing.T) {
	secret, err := adminx.NewTOTPSecret()
	if err != nil {
		t.Fatalf("NewTOTPSecret: %v", err)
	}

	now := time.Now()
	code, err := adminx.TOTPCode(secret, now)
	if err != nil {
		t.Fatalf("TOTPCode: %v", err)
	}

	counter, err := adminx.VerifyTOTP(secret, code, now, 0)
	if err != nil {
		t.Fatalf("VerifyTOTP: %v", err)
	}
	if counter == 0 {
		t.Error("the counter was not returned")
	}

	// A used code must not work again: the counter is remembered next to the user.
	if _, err := adminx.VerifyTOTP(secret, code, now, counter); !errors.Is(err, adminx.ErrCodeReused) {
		t.Errorf("a reused code gave %v", err)
	}

	// A code with the spaces an authenticator app shows is still accepted.
	if _, err := adminx.VerifyTOTP(secret, " "+code+" ", now, 0); err != nil {
		t.Errorf("a code with spaces gave %v", err)
	}
}

// A clock a little off must still let a person in, a clock far off must not.
func TestTOTPClockSkew(t *testing.T) {
	secret, err := adminx.NewTOTPSecret()
	if err != nil {
		t.Fatalf("NewTOTPSecret: %v", err)
	}
	now := time.Now()

	code, err := adminx.TOTPCode(secret, now.Add(-30*time.Second))
	if err != nil {
		t.Fatalf("TOTPCode: %v", err)
	}
	if _, err := adminx.VerifyTOTP(secret, code, now, 0); err != nil {
		t.Errorf("a code from the previous step gave %v", err)
	}

	stale, err := adminx.TOTPCode(secret, now.Add(-5*time.Minute))
	if err != nil {
		t.Fatalf("TOTPCode: %v", err)
	}
	if _, err := adminx.VerifyTOTP(secret, stale, now, 0); !errors.Is(err, adminx.ErrInvalidCode) {
		t.Errorf("an old code gave %v", err)
	}
}

func TestTOTPRejectsBadInput(t *testing.T) {
	secret, err := adminx.NewTOTPSecret()
	if err != nil {
		t.Fatalf("NewTOTPSecret: %v", err)
	}

	for name, code := range map[string]string{
		"empty":     "",
		"too short": "1234",
		"letters":   "abcdef",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := adminx.VerifyTOTP(secret, code, time.Now(), 0); !errors.Is(err, adminx.ErrInvalidCode) {
				t.Fatalf("err = %v", err)
			}
		})
	}

	if _, err := adminx.TOTPCode("not base32!", time.Now()); err == nil {
		t.Error("a broken secret was accepted")
	}
	if _, err := adminx.TOTPCode("", time.Now()); err == nil {
		t.Error("an empty secret was accepted")
	}
}

func TestTOTPURI(t *testing.T) {
	uri := adminx.TOTPURI("shop-api", "admin@example.com", "ABCD")

	for _, want := range []string{
		"otpauth://totp/shop-api:admin@example.com?",
		"secret=ABCD",
		"issuer=shop-api",
		"digits=6",
		"period=30",
	} {
		if !strings.Contains(uri, want) {
			t.Errorf("uri = %q, want %q in it", uri, want)
		}
	}
}
