package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/pluginpb"

	"github.com/aidarbn/platform-go/internal/openapi"
	"google.golang.org/protobuf/types/known/emptypb"
)

func TestRun(t *testing.T) {
	in, err := proto.Marshal(openapi.RequestFor("", emptypb.File_google_protobuf_empty_proto))
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := run(bytes.NewReader(in), &out); err != nil {
		t.Fatal(err)
	}
	resp := &pluginpb.CodeGeneratorResponse{}
	if err := proto.Unmarshal(out.Bytes(), resp); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range resp.GetFile() {
		names = append(names, f.GetName())
	}
	if resp.GetError() != "" || strings.Join(names, ",") != "common.yaml,openapi.bundle.yaml,openapi.yaml" {
		t.Errorf("error = %q, files = %v", resp.GetError(), names)
	}
	if err := run(strings.NewReader("garbage"), &out); err == nil {
		t.Error("garbage is a request")
	}
	if err := run(failing{}, &out); err == nil {
		t.Error("a failed read is not an error")
	}
}

type failing struct{}

func (failing) Read([]byte) (int, error) { return 0, errors.New("broken") }
