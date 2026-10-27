package api

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"google.golang.org/grpc/metadata"
)

// The tenant header comes from the client before authentication: odd values and values
// past the limit share a label value instead of making new series.
func TestTenantLabelIsBounded(t *testing.T) {
	label := tenantFrom("tenant-name")
	call := func(v string) string {
		ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("tenant-name", v))
		return label(ctx)[tenantLabel]
	}
	if got := label(context.Background())[tenantLabel]; got != unknownTenant {
		t.Errorf("no header = %q", got)
	}
	for v, want := range map[string]string{
		"acme-1.kz_A":                       "acme-1.kz_A",
		"":                                  unknownTenant,
		"a b":                               invalidTenant,
		"\xff":                              invalidTenant,
		"ресторан":                          invalidTenant,
		strings.Repeat("x", maxTenantLen):   strings.Repeat("x", maxTenantLen),
		strings.Repeat("x", maxTenantLen+1): invalidTenant,
		`x",y="z`:                           invalidTenant,
	} {
		if got := call(v); got != want {
			t.Errorf("%q = %q, want %q", v, got, want)
		}
	}
	// 2 distinct tenants are already seen above
	for i := range maxTenants - 2 {
		if got := call(fmt.Sprintf("t%d", i)); got != fmt.Sprintf("t%d", i) {
			t.Fatalf("tenant %d = %q", i, got)
		}
	}
	if got := call("one-too-many"); got != otherTenant {
		t.Errorf("past the limit = %q", got)
	}
	if got := call("acme-1.kz_A"); got != "acme-1.kz_A" {
		t.Errorf("a known tenant past the limit = %q", got)
	}
}
