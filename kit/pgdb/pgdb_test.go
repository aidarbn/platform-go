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
	if err == nil || !strings.Contains(err.Error(), "адрес базы не задан") {
		t.Fatalf("err = %v", err)
	}
}

func TestOpenRejectsBadURL(t *testing.T) {
	_, err := pgdb.Open(context.Background(), pgdb.Config{URL: "не адрес"})
	if err == nil || !strings.Contains(err.Error(), "разбор адреса базы") {
		t.Fatalf("err = %v", err)
	}
}

func TestOpenChecksConnection(t *testing.T) {
	// Порт 1 никто не слушает: Open должен вернуть ошибку, а не «рабочий» пул.
	_, err := pgdb.Open(context.Background(), pgdb.Config{
		URL:            "postgres://user:pass@127.0.0.1:1/db?sslmode=disable",
		ConnectTimeout: 2 * time.Second,
	})
	if err == nil || !strings.Contains(err.Error(), "база недоступна") {
		t.Fatalf("err = %v", err)
	}
}
