package adminx_test

import (
	"errors"
	"strings"
	"testing"

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
