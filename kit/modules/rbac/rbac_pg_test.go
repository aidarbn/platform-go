package rbac_test

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/casbin/casbin/v2/model"
	"github.com/casbin/casbin/v2/persist"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aidarbn/platform-go/kit/modules/rbac"
	"github.com/aidarbn/platform-go/kit/pgdb"
)

// pgAdapter is the smallest casbin adapter over Postgres, in the table layout of the
// common casbin adapters: what a project plugs in with WithAdapter.
type pgAdapter struct{ pool *pgxpool.Pool }

func (a pgAdapter) LoadPolicy(m model.Model) error {
	rows, err := a.pool.Query(context.Background(),
		`SELECT ptype, v0, v1, v2, v3 FROM casbin_rule ORDER BY id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var ptype string
		v := make([]string, 4)
		if err := rows.Scan(&ptype, &v[0], &v[1], &v[2], &v[3]); err != nil {
			return err
		}
		for len(v) > 0 && v[len(v)-1] == "" {
			v = v[:len(v)-1]
		}
		if err := persist.LoadPolicyArray(append([]string{ptype}, v...), m); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (a pgAdapter) SavePolicy(model.Model) error { return fmt.Errorf("read only") }
func (a pgAdapter) AddPolicy(sec, ptype string, rule []string) error {
	v := append(append([]string{}, rule...), "", "", "", "")
	_, err := a.pool.Exec(context.Background(),
		`INSERT INTO casbin_rule (ptype, v0, v1, v2, v3) VALUES ($1, $2, $3, $4, $5)`, ptype, v[0], v[1], v[2], v[3])
	return err
}
func (a pgAdapter) RemovePolicy(string, string, []string) error { return fmt.Errorf("read only") }
func (a pgAdapter) RemoveFilteredPolicy(string, string, int, ...string) error {
	return fmt.Errorf("read only")
}

func pgDatabase(t *testing.T) *pgxpool.Pool {
	t.Helper()
	base := os.Getenv("DATABASE_TEST_URL")
	if base == "" {
		t.Skip("DATABASE_TEST_URL is not set")
	}
	ctx := context.Background()
	admin, err := pgdb.Open(ctx, pgdb.Config{URL: base})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	name := fmt.Sprintf("rbac_test_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatalf("create database: %v", err)
	}
	u, _ := url.Parse(base)
	u.Path = "/" + name
	pool, err := pgdb.Open(ctx, pgdb.Config{URL: u.String()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
		admin.Close()
	})
	if _, err := pool.Exec(ctx, `CREATE TABLE casbin_rule (
		id bigserial PRIMARY KEY, ptype text NOT NULL,
		v0 text NOT NULL DEFAULT '', v1 text NOT NULL DEFAULT '', v2 text NOT NULL DEFAULT '', v3 text NOT NULL DEFAULT '')`); err != nil {
		t.Fatal(err)
	}
	return pool
}

// The policy in Postgres: domains and role links in rows, changed by another writer and
// taken by Reload; a change through the casbin API reaches the table.
func TestPostgresAdapter(t *testing.T) {
	pool := pgDatabase(t)
	ctx := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO casbin_rule (ptype, v0, v1, v2, v3) VALUES
		('p', '*', '*', '/grpc.health.v1.Health/*', '*'),
		('p', 'admin', '*', '/shop.v1.Orders/*', '*'),
		('g', 'alice', 'admin', 'shop-1', '')`)

	e, err := rbac.NewEnforcerWith(rbac.WithModel(domainModel), rbac.WithAdapter(pgAdapter{pool}),
		rbac.WithRequest(tenantRequest), rbac.WithPublic(func(m string) []any { return []any{"", "", m, "*"} }))
	if err != nil {
		t.Fatal(err)
	}
	in := func(tenant string) context.Context {
		return context.WithValue(rbac.WithRoles(ctx, "alice"), tenantKey{}, tenant)
	}
	if err := e.Check(in("shop-1"), "/shop.v1.Orders/Delete"); err != nil {
		t.Errorf("alice in shop-1: %v", err)
	}
	if err := e.Check(in("shop-2"), "/shop.v1.Orders/Delete"); err == nil {
		t.Error("alice in shop-2 before the reload")
	}
	if !e.Public("/grpc.health.v1.Health/Check") {
		t.Error("health is public")
	}

	exec(`INSERT INTO casbin_rule (ptype, v0, v1, v2) VALUES ('g', 'alice', 'admin', 'shop-2')`)
	if err := e.Reload(); err != nil {
		t.Fatal(err)
	}
	if err := e.Check(in("shop-2"), "/shop.v1.Orders/Delete"); err != nil {
		t.Errorf("alice in shop-2 after the reload: %v", err)
	}

	if _, err := e.Casbin().AddGroupingPolicy("bob", "admin", "shop-1"); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM casbin_rule WHERE v0 = 'bob'`).Scan(&n); err != nil || n != 1 {
		t.Errorf("the rule added through casbin is not in the table: %d, %v", n, err)
	}

	// The table goes away: the reload fails and the policy in force stays.
	exec(`ALTER TABLE casbin_rule RENAME TO casbin_rule_old`)
	if err := e.Reload(); err == nil || !strings.Contains(err.Error(), "casbin_rule") {
		t.Errorf("Reload without the table: %v", err)
	}
	if err := e.Check(in("shop-2"), "/shop.v1.Orders/Delete"); err != nil {
		t.Errorf("a failed reload changed the policy: %v", err)
	}
}
