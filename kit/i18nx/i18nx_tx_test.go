package i18nx_test

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"google.golang.org/grpc/metadata"

	"github.com/aidarbn/platform-go/kit/i18nx"
	"github.com/aidarbn/platform-go/kit/pgdb"
)

// txDatabase is database with the pool exposed, for tests that run transactions.
func txDatabase(t *testing.T) (*pgxpool.Pool, *i18nx.Client) {
	t.Helper()
	base := os.Getenv("DATABASE_TEST_URL")
	if base == "" {
		t.Skip("DATABASE_TEST_URL is not set")
	}
	ctx := context.Background()
	admin, err := pgdb.Open(ctx, pgdb.Config{URL: base})
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("i18n_tx_test_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
		admin.Close()
	})
	u, _ := url.Parse(base)
	u.Path = "/" + name
	pool, err := pgdb.Open(ctx, pgdb.Config{URL: u.String()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := i18nx.EnsureSchema(ctx, pool); err != nil {
		t.Fatal(err)
	}
	return pool, i18nx.New(pool, i18nx.LocaleRU, i18nx.LocaleKK, i18nx.LocaleEN)
}

// The transaction client writes with the transaction: committed translations are read
// back, rolled back ones are gone.
func TestTxClient(t *testing.T) {
	pool, c := txDatabase(t)
	ctx := context.Background()
	if c.DefaultLocale() != i18nx.LocaleRU {
		t.Fatalf("DefaultLocale = %s", c.DefaultLocale())
	}

	err := pgdb.InTx(ctx, pool, func(tx pgx.Tx) error {
		tc := c.WithTx(tx)
		if err := tc.SetBatch(ctx, "menu", "7", i18nx.Translations{"title": {"kk": "Мәзір", "en": "Menu"}}); err != nil {
			return err
		}
		if err := tc.Set(ctx, "menu", "7", "title", "en", "Main menu"); err != nil {
			return err
		}
		if err := tc.SetBatchIfMissing(ctx, "menu", "7", i18nx.Translations{"title": {"en": "kept?", "tr": "Menü"}}); err != nil {
			return err
		}
		got, err := tc.GetAll(ctx, "menu", "7")
		if err != nil {
			return err
		}
		if got["title"]["en"] != "Main menu" || got["title"]["tr"] != "Menü" || got["title"]["kk"] != "Мәзір" {
			t.Errorf("inside the transaction: %v", got)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := c.GetAll(ctx, "menu", "7"); err != nil || len(got["title"]) != 3 {
		t.Fatalf("after commit: %v, %v", got, err)
	}

	rollback := errors.New("rollback")
	err = pgdb.InTx(ctx, pool, func(tx pgx.Tx) error {
		if err := c.WithTx(tx).Set(ctx, "menu", "8", "title", "en", "Gone"); err != nil {
			return err
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatal(err)
	}
	if got, err := c.GetAll(ctx, "menu", "8"); err != nil || len(got) != 0 {
		t.Fatalf("after rollback: %v, %v", got, err)
	}
}

func TestStorageRejectsAndReports(t *testing.T) {
	pool, c := txDatabase(t)
	ctx := context.Background()

	// Nothing to save and nothing to read are not errors.
	if err := c.SetBatch(ctx, "", "1", nil); err != nil {
		t.Errorf("empty batch: %v", err)
	}
	if got, err := c.GetBatchAll(ctx, "product", nil); err != nil || got != nil {
		t.Errorf("GetBatchAll without ids = %v, %v", got, err)
	}
	if err := c.SetBatch(ctx, "", "1", i18nx.Translations{"name": {"en": "x"}}); !errors.Is(err, i18nx.ErrInvalidArgs) {
		t.Errorf("SetBatch without entity: %v", err)
	}
	if _, err := c.GetAll(ctx, "", "1"); !errors.Is(err, i18nx.ErrInvalidArgs) {
		t.Errorf("GetAll without entity: %v", err)
	}

	// Database failures name the operation.
	if _, err := pool.Exec(ctx, "ALTER TABLE "+i18nx.Table+" ADD CONSTRAINT short_text CHECK (length(text) < 10)"); err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("x", 20)
	if err := c.Set(ctx, "product", "1", "name", "en", long); err == nil || !strings.Contains(err.Error(), "i18n: set:") {
		t.Errorf("Set: %v", err)
	}
	if err := c.SetBatch(ctx, "product", "1", i18nx.Translations{"name": {"en": long}}); err == nil || !strings.Contains(err.Error(), "i18n: set batch") {
		t.Errorf("SetBatch: %v", err)
	}
	if _, err := pool.Exec(ctx, "DROP TABLE "+i18nx.Table); err != nil {
		t.Fatal(err)
	}
	if _, err := c.GetAll(ctx, "product", "1"); err == nil || !strings.Contains(err.Error(), "i18n: get:") {
		t.Errorf("GetAll: %v", err)
	}
	if _, err := c.GetBatchAll(ctx, "product", []string{"1"}); err == nil || !strings.Contains(err.Error(), "i18n: get batch") {
		t.Errorf("GetBatchAll: %v", err)
	}
}

// queriesOnly is a DBTX that cannot start a transaction.
type queriesOnly struct{ i18nx.DBTX }

func TestEnsureSchemaNeedsTransactions(t *testing.T) {
	if err := i18nx.EnsureSchema(context.Background(), queriesOnly{}); err == nil || !strings.Contains(err.Error(), "cannot start a transaction") {
		t.Fatalf("err = %v", err)
	}
	pool, _ := txDatabase(t)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := i18nx.EnsureSchema(cancelled, pool); err == nil || !strings.HasPrefix(err.Error(), "i18n: ") {
		t.Fatalf("cancelled: %v", err)
	}
}

func TestLanguageByCode(t *testing.T) {
	kk, ok := i18nx.LanguageByCode("kk")
	if !ok || kk.NativeName != "Қазақша" {
		t.Fatalf("kk = %+v, %v", kk, ok)
	}
	if _, ok := i18nx.LanguageByCode("xx"); ok {
		t.Fatal("an unknown code was found")
	}
}

// Metadata without accept-language means the default locale.
func TestResolveLocaleWithoutTheHeader(t *testing.T) {
	c := i18nx.New(nil, i18nx.LocaleKK, i18nx.LocaleRU)
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("x-other", "1"))
	if got := c.ResolveLocale(ctx); got != i18nx.LocaleKK {
		t.Fatalf("locale = %s", got)
	}
}
