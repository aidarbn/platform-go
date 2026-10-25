package pgdb_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math/big"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/shopspring/decimal"

	"github.com/aidarbn/platform-go/kit/pgdb"
)

// Code written before the platform registered decimal reads and writes numeric as it did:
// every scan target and parameter gives the same result on a connection with the
// platform types as on a plain one, scanning into any included — rows.Values and
// pgx.RowToMap still get pgtype.Numeric, with NaN and Infinity.
func TestNumericAsWithoutPlatformTypes(t *testing.T) {
	ctx := context.Background()
	url := testURL(t)
	plain, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer plain.Close(ctx)
	pool, err := pgdb.Open(ctx, pgdb.Config{URL: url, MaxConns: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	platform := conn.Conn()

	values := []string{"1.5::numeric", "42::numeric", "'NaN'::numeric", "'Infinity'::numeric", "'-Infinity'::numeric",
		"null::numeric", "12345678901234567890.0123456789::numeric", "1e-30::numeric", "'{1.5,2.25}'::numeric[]"}
	targets := map[string]func() any{
		"string": func() any { return new(string) }, "float64": func() any { return new(float64) },
		"int64": func() any { return new(int64) }, "pgtype.Numeric": func() any { return new(pgtype.Numeric) },
		"[]byte": func() any { return new([]byte) }, "any": func() any { return new(any) },
		"*float64": func() any { return new(*float64) }, "*string": func() any { return new(*string) },
		"sql.NullFloat64": func() any { return new(sql.NullFloat64) }, "sql.NullString": func() any { return new(sql.NullString) },
		"pgtype.Float8": func() any { return new(pgtype.Float8) }, "[]float64": func() any { return new([]float64) },
		"[]string": func() any { return new([]string) }, "[]pgtype.Numeric": func() any { return new([]pgtype.Numeric) },
	}
	read := func(scan func(dst any) error, mk func() any) string {
		dst := mk()
		if err := scan(dst); err != nil {
			return "error: " + err.Error()
		}
		v := reflect.ValueOf(dst).Elem()
		for v.Kind() == reflect.Pointer && !v.IsNil() {
			v = v.Elem()
		}
		j, _ := json.Marshal(v.Interface())
		return fmt.Sprintf("%T %s", v.Interface(), j)
	}
	for _, value := range values {
		for name, mk := range targets {
			q := "select " + value
			want := read(func(d any) error { return plain.QueryRow(ctx, q).Scan(d) }, mk)
			if got := read(func(d any) error { return platform.QueryRow(ctx, q).Scan(d) }, mk); got != want {
				t.Errorf("%s into %s: %s, without platform types %s", value, name, got, want)
			}
		}
		q := "select " + value
		if got, want := rowValues(t, platform, q), rowValues(t, plain, q); got != want {
			t.Errorf("%s through Values: %s, without platform types %s", value, got, want)
		}
	}

	// Stated outright, not only as parity: any and RowToMap give pgtype.Numeric, NaN too.
	for _, q := range []string{"select 'NaN'::numeric as n", "select 1.5::numeric as n"} {
		rows, err := platform.Query(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		m, err := pgx.CollectOneRow(rows, pgx.RowToMap)
		if err != nil {
			t.Fatalf("%s: RowToMap: %v", q, err)
		}
		if _, ok := m["n"].(pgtype.Numeric); !ok {
			t.Errorf("%s: RowToMap gives %T", q, m["n"])
		}
	}

	params := map[string]any{"float64": 1.5, "string": "2.25", "int": 3, "pgtype.Numeric": pgtype.Numeric{Valid: true, Int: bigInt(45), Exp: -1},
		"json.Number": json.Number("4.5"), "nil": nil, "NaN string": "NaN"}
	for name, p := range params {
		var want, got *string
		errP := plain.QueryRow(ctx, "select $1::numeric::text", p).Scan(&want)
		errQ := platform.QueryRow(ctx, "select $1::numeric::text", p).Scan(&got)
		if (errP == nil) != (errQ == nil) || fmt.Sprint(deref(want)) != fmt.Sprint(deref(got)) {
			t.Errorf("parameter %s: %v %v, without platform types %v %v", name, deref(got), errQ, deref(want), errP)
		}
	}

	// database/sql over the pool reads as database/sql over a plain connection.
	db := stdlib.OpenDBFromPool(pool)
	defer db.Close()
	plainDB, err := sql.Open("pgx", url)
	if err != nil {
		t.Fatal(err)
	}
	defer plainDB.Close()
	for _, value := range values {
		for _, name := range []string{"string", "float64", "any", "[]byte", "sql.NullFloat64", "sql.NullString", "*string"} {
			q := "select " + value
			want := read(func(d any) error { return plainDB.QueryRowContext(ctx, q).Scan(d) }, targets[name])
			if got := read(func(d any) error { return db.QueryRowContext(ctx, q).Scan(d) }, targets[name]); got != want {
				t.Errorf("database/sql, %s into %s: %s, without platform types %s", value, name, got, want)
			}
		}
	}
}

// decimal.Decimal and decimal.NullDecimal travel as numeric, scalars and arrays, NULL
// included; NaN does not fit a decimal and fails. These targets go through the scan
// plans of pgx-shopspring-decimal, which RegisterTypes keeps while it puts the codec of
// pgx back for numeric.
func TestDecimalTargets(t *testing.T) {
	ctx := context.Background()
	pool, err := pgdb.Open(ctx, pgdb.Config{URL: testURL(t)})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var d decimal.Decimal
	if err := pool.QueryRow(ctx, "select $1::numeric", decimal.RequireFromString("-0.000123")).Scan(&d); err != nil || d.String() != "-0.000123" {
		t.Errorf("scalar: %s %v", d, err)
	}
	var nd decimal.NullDecimal
	if err := pool.QueryRow(ctx, "select $1::numeric", decimal.NullDecimal{}).Scan(&nd); err != nil || nd.Valid {
		t.Errorf("NULL: %v %v", nd, err)
	}
	if err := pool.QueryRow(ctx, "select $1::numeric", decimal.NewNullDecimal(decimal.RequireFromString("7.25"))).Scan(&nd); err != nil || !nd.Valid || nd.Decimal.String() != "7.25" {
		t.Errorf("NullDecimal: %v %v", nd, err)
	}
	if err := pool.QueryRow(ctx, "select 'NaN'::numeric").Scan(&d); err == nil {
		t.Error("NaN fits a decimal.Decimal")
	}
	var text string
	if err := pool.QueryRow(ctx, "select $1::numeric::text", decimal.RequireFromString("12345678901234567890.0123456789")).Scan(&text); err != nil || text != "12345678901234567890.0123456789" {
		t.Errorf("parameter: %s %v", text, err)
	}
}

// Arrays of numeric travel as decimal.Decimal too.
func TestDecimalArrays(t *testing.T) {
	ctx := context.Background()
	pool, err := pgdb.Open(ctx, pgdb.Config{URL: testURL(t)})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	in := []decimal.Decimal{decimal.RequireFromString("1.5"), decimal.RequireFromString("12345678901234567890.0123456789")}
	var out []decimal.Decimal
	if err := pool.QueryRow(ctx, "select $1::numeric[]", in).Scan(&out); err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 || !out[0].Equal(in[0]) || !out[1].Equal(in[1]) {
		t.Errorf("out = %v", out)
	}
}

func deref(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

func bigInt(v int64) *big.Int { return big.NewInt(v) }

// rowValues returns what rows.Values gives for every row of q, failing on errors.
func rowValues(t *testing.T, conn *pgx.Conn, q string) string {
	t.Helper()
	rows, err := conn.Query(context.Background(), q)
	if err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		v, err := rows.Values()
		if err != nil {
			out = append(out, "error: "+err.Error())
			continue
		}
		out = append(out, fmt.Sprintf("%T %v", v[0], v[0]))
	}
	if err := rows.Err(); err != nil {
		out = append(out, "rows error: "+err.Error())
	}
	return fmt.Sprint(out)
}
