// Command apicover reads an apidiff report on stdin and prints the incompatible changes
// that neither a codemod nor a manual step of docs/upgrades.md answers for, one a line.
//
//	go run ./internal/apicover/cmd/apicover -base v0.8.0 -release v0.9.0 < report
package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/aidarbn/platform-go/internal/apicover"
	"github.com/aidarbn/platform-go/internal/codemod"
)

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "apicover:", err)
		os.Exit(2)
	}
}

func run(args []string, in io.Reader, out io.Writer) error {
	fs := flag.NewFlagSet("apicover", flag.ContinueOnError)
	base := fs.String("base", "", "the released version the report compares against")
	release := fs.String("release", "", "the version being released; empty for any later one")
	docs := fs.String("docs", "docs/upgrades.md", "the manual steps")
	if err := fs.Parse(args); err != nil {
		return err
	}
	report, err := io.ReadAll(in)
	if err != nil {
		return err
	}
	manual, err := os.ReadFile(*docs)
	if err != nil {
		return err
	}
	changes := apicover.Incompatible(string(report), codemod.Module)
	for _, name := range apicover.Uncovered(changes, codemod.All(), apicover.Manual(string(manual)), *base, *release) {
		fmt.Fprintln(out, name)
	}
	return nil
}
