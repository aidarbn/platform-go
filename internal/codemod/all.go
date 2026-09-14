package codemod

// All are the codemods of the platform, oldest first. Add one with the change of kit
// that needs it, under the version that will be released with it:
//
//	RenameSymbol("v0.3.0", "github.com/aidarbn/platform-go/kit/modules/riverx", "NewQueue", "Queue"),
func All() []Codemod {
	return []Codemod{}
}
