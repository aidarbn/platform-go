package ptrx_test

import (
	"testing"

	"github.com/aidarbn/platform-go/kit/ptrx"
)

func TestEqual(t *testing.T) {
	one, anotherOne, two := int64(1), int64(1), int64(2)

	tests := []struct {
		name string
		a, b *int64
		want bool
	}{
		{"both unset", nil, nil, true},
		{"only the first set", &one, nil, false},
		{"only the second set", nil, &one, false},
		{"different values", &one, &two, false},
		// What the function exists for: compare by value, not by address.
		{"equal values in different variables", &one, &anotherOne, true},
		{"the same pointer", &one, &one, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ptrx.Equal(tc.a, tc.b); got != tc.want {
				t.Fatalf("Equal = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestEqualStrings(t *testing.T) {
	empty, value := "", "bin"

	if !ptrx.Equal(&empty, &empty) {
		t.Error("an empty string equals itself")
	}
	if ptrx.Equal(&empty, nil) {
		t.Error("an empty string is set, which is not the same as unset")
	}
	if ptrx.Equal(&empty, &value) {
		t.Error("different strings are not equal")
	}
}

func TestValueOr(t *testing.T) {
	v := 5
	if got := ptrx.ValueOr(&v, 1); got != 5 {
		t.Errorf("ValueOr(&5, 1) = %d", got)
	}
	if got := ptrx.ValueOr(nil, 1); got != 1 {
		t.Errorf("ValueOr(nil, 1) = %d", got)
	}
}

func TestPrefer(t *testing.T) {
	a, b := 1, 2
	if got := ptrx.Prefer(&a, &b); got != &a {
		t.Error("the primary wins when set")
	}
	if got := ptrx.Prefer(nil, &b); got != &b {
		t.Error("the fallback is used when the primary is nil")
	}
	if got := ptrx.Prefer[int](nil, nil); got != nil {
		t.Error("both nil gives nil")
	}
}

func TestNonZero(t *testing.T) {
	if got := ptrx.NonZero(""); got != nil {
		t.Errorf("NonZero(\"\") = %v, want nil", got)
	}
	if got := ptrx.NonZero(0); got != nil {
		t.Errorf("NonZero(0) = %v, want nil", got)
	}
	if got := ptrx.NonZero("a"); got == nil || *got != "a" {
		t.Errorf("NonZero(\"a\") = %v", got)
	}
}
