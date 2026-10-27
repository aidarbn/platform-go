package pgdb_test

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/aidarbn/platform-go/kit/pgdb"
)

func testURL(t *testing.T) string {
	t.Helper()
	url := os.Getenv("DATABASE_TEST_URL")
	if url == "" {
		t.Skip("DATABASE_TEST_URL is not set")
	}
	return url
}

// numeric must come back as decimal.Decimal without losing digits: the generated sqlc
// config maps numeric to it. The codec is registered on the connection, so values travel
// in the binary format rather than through the text fallback of sql.Scanner.
func TestDecimalRoundTrip(t *testing.T) {
	ctx := context.Background()
	pool, err := pgdb.Open(ctx, pgdb.Config{URL: testURL(t)})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	// A temp table lives on one connection, so the whole check runs on one.
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	if _, ok := conn.Conn().TypeMap().TypeForValue(decimal.Decimal{}); !ok {
		t.Fatal("decimal.Decimal is not registered on the connection")
	}
	if _, err := conn.Exec(ctx, `create temp table pr3_money (id int primary key, amount numeric(30,10), discount numeric)`); err != nil {
		t.Fatal(err)
	}

	amount := decimal.RequireFromString("12345678901234567890.0123456789") // beyond float64
	if _, err := conn.Exec(ctx, `insert into pr3_money values ($1, $2, $3)`, 1, amount, nil); err != nil {
		t.Fatalf("write decimal: %v", err)
	}
	var (
		got      decimal.Decimal
		discount decimal.NullDecimal
		text     string
	)
	if err := conn.QueryRow(ctx, `select amount, discount, amount::text from pr3_money where id = 1`).Scan(&got, &discount, &text); err != nil {
		t.Fatalf("read decimal: %v", err)
	}
	if !got.Equal(amount) || text != "12345678901234567890.0123456789" {
		t.Errorf("amount = %s (%s), want %s", got, text, amount)
	}
	if discount.Valid {
		t.Errorf("discount = %v, want NULL", discount)
	}

	var pct *decimal.Decimal
	if err := conn.QueryRow(ctx, `select 12.50::numeric`).Scan(&pct); err != nil {
		t.Fatalf("read into a pointer: %v", err)
	}
	if pct == nil || !pct.Equal(decimal.RequireFromString("12.5")) {
		t.Errorf("pct = %v", pct)
	}
}

// The hook runs on every connection the pool opens, after the platform types.
func TestAfterConnectRunsOnEveryConnection(t *testing.T) {
	ctx := context.Background()
	var calls atomic.Int32
	pool, err := pgdb.Open(ctx, pgdb.Config{
		URL:      testURL(t),
		MaxConns: 4,
		Hooks: &pgdb.Hooks{AfterConnect: func(_ context.Context, conn *pgx.Conn) error {
			if _, ok := conn.TypeMap().TypeForValue(decimal.Decimal{}); !ok {
				return errors.New("decimal is not registered before the project hook")
			}
			calls.Add(1)
			return nil
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	// Holding three connections at once makes the pool open three.
	var conns []*pgxpool.Conn
	for range 3 {
		c, err := pool.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		conns = append(conns, c)
	}
	for _, c := range conns {
		c.Release()
	}
	// A connection counts before its hooks have run, so wait for none to be in the making.
	deadline := time.Now().Add(5 * time.Second)
	for pool.Stat().ConstructingConns() > 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got, opened := calls.Load(), pool.Stat().TotalConns(); got < 3 || got != opened {
		t.Errorf("hook calls = %d, connections = %d", got, opened)
	}
}

// ConfigurePool sees the applied sizes and may add its own AfterConnect: it runs after the
// platform types and before Hooks.AfterConnect.
func TestConfigurePool(t *testing.T) {
	ctx := context.Background()
	var (
		mu    sync.Mutex
		order []string
	)
	record := func(s string) {
		mu.Lock()
		defer mu.Unlock()
		order = append(order, s)
	}
	pool, err := pgdb.Open(ctx, pgdb.Config{
		URL:      testURL(t),
		MaxConns: 3,
		Hooks: &pgdb.Hooks{
			ConfigurePool: func(c *pgxpool.Config) {
				if c.MaxConns != 3 {
					t.Errorf("ConfigurePool sees MaxConns = %d", c.MaxConns)
				}
				c.ConnConfig.RuntimeParams["application_name"] = "platformgo-pr3"
				c.AfterConnect = func(context.Context, *pgx.Conn) error { record("pool"); return nil }
			},
			AfterConnect: func(context.Context, *pgx.Conn) error { record("config"); return nil },
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	var name string
	if err := pool.QueryRow(ctx, `show application_name`).Scan(&name); err != nil {
		t.Fatal(err)
	}
	if name != "platformgo-pr3" {
		t.Errorf("application_name = %q", name)
	}
	var d decimal.Decimal
	if err := pool.QueryRow(ctx, `select 1.5::numeric`).Scan(&d); err != nil {
		t.Errorf("ConfigurePool dropped the platform types: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(order) < 2 || !slices.Equal(order[:2], []string{"pool", "config"}) {
		t.Errorf("hook order = %v", order)
	}
}

// A failing hook discards the connection, so Open reports the database as unusable.
func TestAfterConnectErrorFailsOpen(t *testing.T) {
	_, err := pgdb.Open(context.Background(), pgdb.Config{
		URL:   testURL(t),
		Hooks: &pgdb.Hooks{AfterConnect: func(context.Context, *pgx.Conn) error { return errors.New("no such type") }},
	})
	if err == nil || !strings.Contains(err.Error(), "no such type") {
		t.Fatalf("err = %v", err)
	}
}

// RegisterTypes serves connections made without Open.
func TestRegisterTypesOnPlainConnection(t *testing.T) {
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, testURL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	pgdb.RegisterTypes(conn)

	var d decimal.Decimal
	if err := conn.QueryRow(ctx, `select 0.1::numeric + 0.2::numeric`).Scan(&d); err != nil {
		t.Fatal(err)
	}
	if !d.Equal(decimal.RequireFromString("0.3")) {
		t.Errorf("d = %s", d)
	}
}

// Config stays comparable: code that compares two configs with == keeps compiling.
func TestConfigComparable(t *testing.T) {
	hooks := &pgdb.Hooks{}
	a, b := pgdb.Config{URL: "x", Hooks: hooks}, pgdb.Config{URL: "x", Hooks: hooks}
	if a != b || a == (pgdb.Config{URL: "x"}) {
		t.Error("configs compare wrong")
	}
}
