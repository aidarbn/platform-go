// Command protoc-gen-platform-openapi is the OpenAPI generator of the platform, a buf
// plugin. It writes OpenAPI 3.1 of the REST API the api module serves, merged into the
// project's api/openapi/base.yaml:
//
//	plugins:
//	  - local: [go, tool, protoc-gen-platform-openapi]
//	    out: api/openapi
//	    strategy: all
//	    opt: [base=api/openapi/base.yaml, path=openapi.yaml]
//
// With documents=platformgo.yaml it also writes openapi.<name>.bundle.yaml for every
// document of api.openapi.documents in that file, a description for one audience each.
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/aidarbn/platform-go/internal/openapi"
)

func main() {
	if err := run(os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "protoc-gen-platform-openapi:", err)
		os.Exit(1)
	}
}

func run(in io.Reader, out io.Writer) error {
	req, err := io.ReadAll(in)
	if err != nil {
		return err
	}
	resp, err := openapi.Run(req)
	if err != nil {
		return err
	}
	_, err = out.Write(resp)
	return err
}
