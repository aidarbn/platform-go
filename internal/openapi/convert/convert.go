// Package convert moves the openapiv2 options of grpc-gateway (protoc-gen-openapiv2)
// in proto files to what the platform's OpenAPI generator reads: descriptions into
// proto comments, examples, tags and hidden methods into platform.openapi.v1 options,
// the file-level openapiv2_swagger into api/openapi/base.yaml. It runs once on a
// project that moves to the platform; whatever it cannot move stays as it was and is
// reported for a person.
package convert

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/bufbuild/protocompile"
	"github.com/bufbuild/protocompile/linker"
	"github.com/bufbuild/protocompile/protoutil"
	_ "github.com/grpc-ecosystem/grpc-gateway/v2/protoc-gen-openapiv2/options"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/aidarbn/platform-go/internal/codemod"

	// Imports of proto files a project commonly has, resolved from the binary.
	_ "buf.build/gen/go/bufbuild/protovalidate/protocolbuffers/go/buf/validate"
	_ "github.com/aidarbn/platform-go/kit/i18nx/i18npb"
	_ "github.com/aidarbn/platform-go/kit/openapipb"
	_ "google.golang.org/genproto/googleapis/api/annotations"
	_ "google.golang.org/genproto/googleapis/api/httpbody"
	_ "google.golang.org/protobuf/types/known/anypb"
	_ "google.golang.org/protobuf/types/known/durationpb"
	_ "google.golang.org/protobuf/types/known/emptypb"
	_ "google.golang.org/protobuf/types/known/fieldmaskpb"
	_ "google.golang.org/protobuf/types/known/structpb"
	_ "google.golang.org/protobuf/types/known/timestamppb"
	_ "google.golang.org/protobuf/types/known/wrapperspb"
)

// The options the converter reads and the one it writes.
const (
	gwPrefix      = "grpc.gateway.protoc_gen_openapiv2.options."
	gwField       = gwPrefix + "openapiv2_field"
	gwOperation   = gwPrefix + "openapiv2_operation"
	gwSwagger     = gwPrefix + "openapiv2_swagger"
	gwSchema      = gwPrefix + "openapiv2_schema"
	gwTag         = gwPrefix + "openapiv2_tag"
	gwImport      = "protoc-gen-openapiv2/options/annotations.proto"
	gwImportTypes = "protoc-gen-openapiv2/options/openapiv2.proto"
	ownImport     = "platform/openapi/v1/options.proto"
)

// Options say where the proto files are and where the base goes.
type Options struct {
	Dir      string // the project
	ProtoDir string // the proto folder, relative to Dir: imports are relative to it
	Base     string // api/openapi/base.yaml, relative to Dir
	DryRun   bool   // report only, write nothing
}

// Note is a line of the report.
type Note struct {
	Path    string // relative to Dir
	Line    int
	Manual  bool // a person must look: the converter left something as it was
	Message string
}

func (n Note) String() string {
	kind := "note"
	if n.Manual {
		kind = "manual"
	}
	if n.Line == 0 {
		return fmt.Sprintf("%s %s: %s", kind, n.Path, n.Message)
	}
	return fmt.Sprintf("%s %s:%d: %s", kind, n.Path, n.Line, n.Message)
}

// Report is what the conversion did.
type Report struct {
	Changed []string       // proto files rewritten, relative to Dir
	Base    string         // the file the openapiv2_swagger options went to, empty without any
	Counts  map[string]int // what was moved, by kind
	Notes   []Note
}

func (r *Report) count(kind string) { r.Counts[kind]++ }

func (r *Report) note(p string, line int, manual bool, format string, args ...any) {
	r.Notes = append(r.Notes, Note{Path: p, Line: line, Manual: manual, Message: fmt.Sprintf(format, args...)})
}

// Run converts the proto files of a project.
func Run(o Options) (*Report, error) {
	protoRoot := filepath.Join(o.Dir, filepath.FromSlash(o.ProtoDir))
	names, err := protoFiles(protoRoot)
	if err != nil {
		return nil, err
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("no proto files in %s", o.ProtoDir)
	}
	files, err := describe(protoRoot, names, nil)
	if err != nil {
		return nil, err
	}
	r := &Report{Counts: map[string]int{}}
	out := map[string][]byte{}
	var swaggers []fileSwagger
	for _, name := range names {
		fd, err := files.FindFileByPath(name)
		if err != nil {
			return nil, err
		}
		rel := path.Join(o.ProtoDir, name)
		src, err := os.ReadFile(filepath.Join(protoRoot, filepath.FromSlash(name)))
		if err != nil {
			return nil, err
		}
		c := &fileConv{rel: rel, src: src, pf: codemod.ParseProto(rel, src), fd: fd, r: r}
		c.lines()
		if err := c.run(); err != nil {
			return nil, fmt.Errorf("%s: %w", rel, err)
		}
		if c.swagger != nil {
			swaggers = append(swaggers, fileSwagger{path: rel, swagger: c.swagger})
		}
		changed, err := c.pf.Result()
		if err != nil {
			return nil, fmt.Errorf("%s: %w", rel, err)
		}
		if !bytes.Equal(changed, src) {
			out[name] = changed
			r.Changed = append(r.Changed, rel)
		}
	}
	if len(out) > 0 {
		if _, err := os.Stat(filepath.Join(protoRoot, "platform", "openapi", "v1", "options.proto")); err != nil {
			r.note(path.Join(o.ProtoDir, ownImport), 0, false, "the converted files import it: set openapi.generator: platform for the api module in platformgo.yaml, and platformgo apply writes it")
		}
	}
	// The result must compile, with the platform's options in place of the old ones.
	if len(out) > 0 {
		if _, err := describe(protoRoot, names, out); err != nil {
			return nil, fmt.Errorf("the converted proto files do not compile, nothing is written: %w", err)
		}
	}
	var base []byte
	if len(swaggers) > 0 {
		if base, err = baseYAML(swaggers, r); err != nil {
			return nil, err
		}
		r.Base = o.Base
		if _, err := os.Stat(filepath.Join(o.Dir, filepath.FromSlash(o.Base))); err == nil {
			r.Base = strings.TrimSuffix(o.Base, ".yaml") + ".openapiv2.yaml"
			r.note(r.Base, 0, true, "%s exists: merge info, security, tags and x-tagGroups from this file into it by hand", o.Base)
		}
	}
	if o.DryRun {
		return r, nil
	}
	for name, content := range out {
		if err := os.WriteFile(filepath.Join(protoRoot, filepath.FromSlash(name)), content, 0o644); err != nil {
			return nil, err
		}
	}
	if base != nil {
		p := filepath.Join(o.Dir, filepath.FromSlash(r.Base))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(p, base, 0o644); err != nil {
			return nil, err
		}
	}
	return r, nil
}

// protoFiles lists the proto files under the folder, the platform's own left out.
func protoFiles(root string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() && rel != "." && (rel == "platform" || strings.HasPrefix(d.Name(), ".")) {
			return fs.SkipDir
		}
		if !d.IsDir() && strings.HasSuffix(p, ".proto") {
			out = append(out, rel)
		}
		return nil
	})
	slices.Sort(out)
	return out, err
}

// describe compiles the proto files with their comments and positions, the sources
// in override instead of the files on disk, and returns them with their options
// decoded into the types the binary knows.
func describe(root string, names []string, override map[string][]byte) (*protoregistry.Files, error) {
	c := protocompile.Compiler{
		Resolver: protocompile.CompositeResolver{
			&protocompile.SourceResolver{
				ImportPaths: []string{root},
				Accessor: func(p string) (io.ReadCloser, error) {
					rel, err := filepath.Rel(root, p)
					if err == nil {
						if src, ok := override[filepath.ToSlash(rel)]; ok {
							return io.NopCloser(bytes.NewReader(src)), nil
						}
					}
					return os.Open(p)
				},
			},
			protocompile.ResolverFunc(func(p string) (protocompile.SearchResult, error) {
				fd, err := protoregistry.GlobalFiles.FindFileByPath(p)
				if err != nil {
					return protocompile.SearchResult{}, fmt.Errorf("%s: not in the proto folder and not known to platformgo", p)
				}
				return protocompile.SearchResult{Desc: fd}, nil
			}),
		},
		SourceInfoMode: protocompile.SourceInfoStandard,
	}
	compiled, err := c.Compile(context.Background(), names...)
	if err != nil {
		return nil, err
	}
	set := &descriptorpb.FileDescriptorSet{}
	seen := map[string]bool{}
	var add func(f protoreflect.FileDescriptor)
	add = func(f protoreflect.FileDescriptor) {
		if seen[f.Path()] {
			return
		}
		seen[f.Path()] = true
		for i := range f.Imports().Len() {
			add(f.Imports().Get(i).FileDescriptor)
		}
		if _, ok := f.(linker.File); ok {
			set.File = append(set.File, protoutil.ProtoFromFileDescriptor(f))
		} else {
			set.File = append(set.File, protodesc.ToFileDescriptorProto(f))
		}
	}
	for _, f := range compiled {
		add(f)
	}
	// Through bytes, so the options become the Go types of their extensions.
	raw, err := proto.Marshal(set)
	if err != nil {
		return nil, err
	}
	typed := &descriptorpb.FileDescriptorSet{}
	if err := proto.Unmarshal(raw, typed); err != nil {
		return nil, err
	}
	return protodesc.NewFiles(typed)
}
