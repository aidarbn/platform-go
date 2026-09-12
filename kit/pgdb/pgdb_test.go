package pgdb_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/aidarbn/platform-go/kit/pgdb"
)

func TestOpenRequiresURL(t *testing.T) {
	_, err := pgdb.Open(context.Background(), pgdb.Config{})
	if err == nil || !strings.Contains(err.Error(), "database url is not set") {
		t.Fatalf("err = %v", err)
	}
}

func TestOpenRejectsBadURL(t *testing.T) {
	_, err := pgdb.Open(context.Background(), pgdb.Config{URL: "not a url"})
	if err == nil || !strings.Contains(err.Error(), "parse database url") {
		t.Fatalf("err = %v", err)
	}
}

func TestOpenChecksConnection(t *testing.T) {
	// Nothing listens on port 1: Open must fail instead of handing back a broken pool.
	_, err := pgdb.Open(context.Background(), pgdb.Config{
		URL:            "postgres://user:pass@127.0.0.1:1/db?sslmode=disable",
		ConnectTimeout: 2 * time.Second,
	})
	if err == nil || !strings.Contains(err.Error(), "database is unreachable") {
		t.Fatalf("err = %v", err)
	}
}
