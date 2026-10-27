package admin_test

import (
	"context"
	"testing"

	"github.com/aidarbn/platform-go/kit/modules/admin"
	"github.com/aidarbn/platform-go/kit/platform"
)

// The panel is an HTTP server of its own, so a phased shutdown stops it with the others.
func TestStopPhases(t *testing.T) {
	phases := admin.New(admin.Config{}).StopPhases()
	if len(phases) != 1 || phases[0].Name != "http" || phases[0].Order != platform.PhaseHTTP {
		t.Fatalf("phases = %+v", phases)
	}
	// A panel that never started stops cleanly through its phase.
	if err := phases[0].Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}
