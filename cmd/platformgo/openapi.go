package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"

	"github.com/aidarbn/platform-go/internal/apply"
	"github.com/aidarbn/platform-go/internal/gen"
	"github.com/aidarbn/platform-go/internal/openapi/convert"
	"github.com/aidarbn/platform-go/internal/spec"
)

// cmdOpenAPI is platformgo openapi: tools for the OpenAPI description.
func cmdOpenAPI(args []string, out io.Writer) error {
	if len(args) == 0 || args[0] != "convert" {
		return errors.New("usage: platformgo openapi convert [--dir .] [--proto dir] [--base api/openapi/base.yaml] [--dry-run]")
	}
	fs := flag.NewFlagSet("openapi convert", flag.ContinueOnError)
	dir := fs.String("dir", ".", "project directory")
	protoDir := fs.String("proto", "", "the proto folder, relative to the project; proto_dir of the api module, or proto")
	base := fs.String("base", gen.OpenAPIBasePath, "where info, security, tags and x-tagGroups of openapiv2_swagger go, relative to the project")
	dryRun := fs.Bool("dry-run", false, "report what would change, write nothing")
	if err := parseFlags(fs, args[1:]); err != nil {
		return err
	}
	if *protoDir == "" {
		*protoDir = "proto"
		// A project of the platform knows its folder; a project moving to it may
		// have no platformgo.yaml yet.
		if _, err := os.Stat(filepath.Join(*dir, spec.FileName)); err == nil {
			plan, err := apply.Build(*dir)
			if err != nil {
				return err
			}
			if plan.API.ProtoDir != "" {
				*protoDir = plan.API.ProtoDir
			}
		}
	}
	r, err := convert.Run(convert.Options{Dir: *dir, ProtoDir: *protoDir, Base: *base, DryRun: *dryRun})
	if err != nil {
		return err
	}
	verb := "changed"
	if *dryRun {
		verb = "would change"
	}
	for _, p := range r.Changed {
		fmt.Fprintf(out, "%s %s\n", verb, p)
	}
	if r.Base != "" {
		fmt.Fprintf(out, "%s %s\n", map[bool]string{true: "would write", false: "wrote"}[*dryRun], r.Base)
	}
	var kinds []string
	for k := range r.Counts {
		kinds = append(kinds, k)
	}
	slices.Sort(kinds)
	for _, k := range kinds {
		fmt.Fprintf(out, "%5d %s\n", r.Counts[k], k)
	}
	manual := 0
	for _, n := range r.Notes {
		fmt.Fprintln(out, n)
		if n.Manual {
			manual++
		}
	}
	if manual > 0 {
		fmt.Fprintf(out, "%d places need a person: the lines marked manual\n", manual)
	}
	return nil
}
