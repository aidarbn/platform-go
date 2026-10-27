package codemod

// All are the codemods of the platform, oldest first. Add one with the change of kit
// that needs it, under the version that will be released with it:
//
//	RenameSymbol("v0.3.0", "github.com/aidarbn/platform-go/kit/modules/riverx", "NewQueue", "Queue"),
//
// An incompatible change no codemod can follow is described under its version in the
// manual steps of docs/upgrades.md instead; scripts/apicheck.sh wants one of the two.
func All() []Codemod {
	return []Codemod{
		AdminxNoTOTP(),
		GnosticOptions(),
	}
}
