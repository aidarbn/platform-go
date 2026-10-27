package riverx_test

import (
	"context"
	"testing"

	"github.com/aidarbn/platform-go/kit/modules/postgres"
	"github.com/aidarbn/platform-go/kit/modules/riverx"
	"github.com/aidarbn/platform-go/kit/platform"
)

// The River client exists from Start on: wireDomain sees none, a running service has one
// and reports healthy.
func TestClientAndHealth(t *testing.T) {
	m := riverx.New(riverx.Config{})
	if err := m.Health(context.Background()); err == nil {
		t.Fatal("health before Start must fail")
	}

	dsn := database(t)
	var queue *riverx.Queue
	var inWire bool
	run(t, []platform.Module{postgres.New(postgres.Config{URL: dsn}), m}, func(app *platform.App) error {
		queue = riverx.QueueFrom(app)
		inWire = queue.Client() != nil
		return nil
	})
	if inWire {
		t.Error("the client exists before Start")
	}
	if queue.Client() == nil {
		t.Fatal("no client after Start")
	}
	if err := m.Health(context.Background()); err != nil {
		t.Fatalf("health: %v", err)
	}
}
