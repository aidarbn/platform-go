package apply

import (
	"testing"

	"github.com/aidarbn/platform-go/internal/codemod"
)

// SetCodemods replaces the codemods of the platform for one test.
func SetCodemods(t *testing.T, mods ...codemod.Codemod) {
	old := codemods
	codemods = func() []codemod.Codemod { return mods }
	t.Cleanup(func() { codemods = old })
}
