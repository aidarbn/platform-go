package gen_test

import (
	"strings"
	"testing"
	"testing/fstest"

	"github.com/aidarbn/platform-go/internal/gen"
)

const withEnums = "schema: 1\nproject:\n  module: github.com/aidarbn/shop-api\nmodules:\n  api: {}\n  enums: {}\n"

const orderGo = `package domain

type PaymentStatus string

// go-enum: payment.status "Payment status" entity="Payments" order=20
const (
	PaymentPending PaymentStatus = "pending" // waiting for the customer
	PaymentPaid    PaymentStatus = "paid"    // money received
)

// go-enum: order.status "Order status" entity="Orders" order=10
const (
	OrderNew       = "new"       // just created
	OrderCancelled = "cancelled" // cancelled by the customer
)

// go-enum: payment.provider "Payment provider" entity="Payments" order=20
const (
	ProviderKaspi = "kaspi_qr" // Kaspi QR
)

const unrelated = 1
`

func TestEnumsCode(t *testing.T) {
	project := fstest.MapFS{
		"internal/domain/order.go":      {Data: []byte(orderGo)},
		"internal/domain/order_test.go": {Data: []byte("package domain\n\n// go-enum: x.y \"ignored\" entity=\"X\" order=1\nconst ( A = \"a\" )\n")},
	}
	code, err := gen.EnumsCode(mustParse(t, withEnums), project)
	if err != nil {
		t.Fatalf("EnumsCode: %v", err)
	}
	got := string(code)
	for _, want := range []string{
		`"github.com/aidarbn/shop-api/internal/domain"`,
		`{Value: domain.OrderNew, Description: "just created"}`,
		`{Value: string(domain.PaymentPending), Description: "waiting for the customer"}`,
		`Name:        "provider"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("enums.gen.go lacks %q:\n%s", want, got)
		}
	}
	// Entities follow order=: orders (10) before payments (20); test files are ignored.
	if strings.Index(got, `Entity:      "order"`) > strings.Index(got, `Entity:      "payment"`) {
		t.Errorf("entities out of order:\n%s", got)
	}
	if strings.Contains(got, `"x"`) {
		t.Error("a marker from a test file was used")
	}
}

func TestEnumsCodeWithoutMarkers(t *testing.T) {
	code, err := gen.EnumsCode(mustParse(t, withEnums), fstest.MapFS{})
	if err != nil {
		t.Fatalf("EnumsCode: %v", err)
	}
	if got := string(code); strings.Contains(got, "internal/domain") || !strings.Contains(got, "var Catalog = enumx.Catalog{}") {
		t.Errorf("an empty catalog must not import the domain:\n%s", got)
	}
}

func TestEnumsCodeErrors(t *testing.T) {
	cases := map[string]struct {
		src  string
		want string
	}{
		"entity mismatch": {
			"package domain\n// go-enum: order.status \"S\" entity=\"Orders\" order=10\nconst ( A = \"a\" )\n// go-enum: order.kind \"K\" entity=\"Other\" order=10\nconst ( B = \"b\" )\n",
			`declares entity="Other"`,
		},
		"duplicate enum": {
			"package domain\n// go-enum: order.status \"S\" entity=\"Orders\" order=10\nconst ( A = \"a\" )\n// go-enum: order.status \"S\" entity=\"Orders\" order=10\nconst ( B = \"b\" )\n",
			"duplicate go-enum order.status",
		},
		"shared line": {
			"package domain\n// go-enum: order.status \"S\" entity=\"Orders\" order=10\nconst ( A, B = \"a\", \"b\" )\n",
			"its own name and value",
		},
		"broken file": {"package domain\nconst (\n", "parse"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := gen.EnumsCode(mustParse(t, withEnums), fstest.MapFS{"internal/domain/x.go": {Data: []byte(tc.src)}})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestEnumsDomainOption(t *testing.T) {
	f := mustParse(t, strings.Replace(withEnums, "enums: {}", "enums:\n    domain: pkg/model", 1))
	code, err := gen.EnumsCode(f, fstest.MapFS{"pkg/model/order.go": {Data: []byte(strings.Replace(orderGo, "package domain", "package model", 1))}})
	if err != nil {
		t.Fatalf("EnumsCode: %v", err)
	}
	if got := string(code); !strings.Contains(got, `"github.com/aidarbn/shop-api/pkg/model"`) || !strings.Contains(got, "model.OrderNew") {
		t.Errorf("enums.gen.go:\n%s", got)
	}
}
