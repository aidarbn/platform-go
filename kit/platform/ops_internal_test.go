package platform

import (
	"log/slog"
	"testing"
)

// The ops server bounds whole requests and closes idle connections, on top of the
// header timeout: a slow client cannot hold a probe port.
func TestOpsServerTimeouts(t *testing.T) {
	app := &App{log: slog.New(slog.DiscardHandler)}
	s, err := startOps("127.0.0.1:0", false, app)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.srv.Close() })
	if s.srv.ReadHeaderTimeout != readHeaderTimeout || s.srv.ReadTimeout != opsReadTimeout ||
		s.srv.WriteTimeout != opsWriteTimeout || s.srv.IdleTimeout != opsIdleTimeout {
		t.Fatalf("timeouts: %v %v %v %v", s.srv.ReadHeaderTimeout, s.srv.ReadTimeout, s.srv.WriteTimeout, s.srv.IdleTimeout)
	}
}
