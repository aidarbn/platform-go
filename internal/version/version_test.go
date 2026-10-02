package version

import (
	"runtime/debug"
	"testing"
)

func TestFromBuildInfo(t *testing.T) {
	dep := func(v string, replace *debug.Module) *debug.Module {
		return &debug.Module{Path: Module, Version: v, Replace: replace}
	}
	tests := []struct {
		name string
		info debug.BuildInfo
		want string
	}{
		{"installed at a version", debug.BuildInfo{Main: debug.Module{Path: Module, Version: "v0.6.1"}}, "v0.6.1"},
		{"built from a checkout", debug.BuildInfo{Main: debug.Module{Path: Module, Version: "(devel)"}}, Fallback},
		{"go tool inside a project", debug.BuildInfo{Main: *dep("v0.6.2", nil)}, "v0.6.2"},
		{"go tool with a replaced platform", debug.BuildInfo{Main: *dep("v0.0.0", &debug.Module{Path: "../platform-go", Version: "(devel)"})}, Fallback},
		{"platform as a dependency", debug.BuildInfo{
			Main: debug.Module{Path: "github.com/acme/shop", Version: "(devel)"},
			Deps: []*debug.Module{{Path: "github.com/google/uuid", Version: "v1.6.0"}, dep("v0.6.2", nil)},
		}, "v0.6.2"},
		{"replaced by a local checkout", debug.BuildInfo{
			Main: debug.Module{Path: "github.com/acme/shop"},
			Deps: []*debug.Module{dep("v0.6.2", &debug.Module{Path: "../platform-go"})},
		}, Fallback},
		{"pseudo-version", debug.BuildInfo{Main: debug.Module{Path: Module, Version: "v0.6.2-0.20261002120000-abcdefabcdef"}}, Fallback},
		{"platform absent", debug.BuildInfo{Main: debug.Module{Path: "github.com/acme/shop"}}, Fallback},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := fromBuildInfo(&tt.info); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}
