package api_test

import (
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/aidarbn/platform-go/internal/openapi"
	"github.com/aidarbn/platform-go/internal/openapi/oascheck"
	testv1 "github.com/aidarbn/platform-go/kit/internal/testapi/platformtest/v1"
	"github.com/aidarbn/platform-go/kit/modules/api"
)

func (echo) Kinds(_ context.Context, req *testv1.KindsRequest) (*testv1.KindsResponse, error) {
	if !req.GetFull() {
		return &testv1.KindsResponse{Id: req.GetId()}, nil
	}
	extra, _ := structpb.NewStruct(map[string]any{"a": 1, "b": []any{"x", true}})
	packed, _ := anypb.New(&testv1.Meta{Author: "aidar"})
	return &testv1.KindsResponse{
		Id: req.GetId(), Big: 1 << 63, Small: -3, Ratio: 0.5, Part: 1.25, Flag: true, Text: "text", Data: []byte{0, 1, 2},
		Color: req.GetColor(), Tags: req.GetTags(), Counts: map[string]int64{"a": 1}, Meta: &testv1.Meta{Author: "aidar"},
		Note: new("note"), Choice: &testv1.KindsResponse_Word{Word: "word"},
		At: timestamppb.New(time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)), Took: durationpb.New(1500 * time.Millisecond),
		Maybe: wrapperspb.String("maybe"), Extra: extra, Anything: structpb.NewNumberValue(2),
		Metas: []*testv1.Meta{{Author: "a"}, {}}, Mask: &fieldmaskpb.FieldMask{Paths: []string{"message_text", "meta.author"}},
		Packed: packed, Nothing: &emptypb.Empty{}, ByNumber: map[int32]*testv1.Meta{7: {Author: "b"}},
		Negative: -7, Unsigned: 7,
	}, nil
}

// The description of the test service, generated from the same proto files the gateway
// serves: every answer of the gateway must match the schema the description gives for
// it, so the description cannot drift from what the gateway really writes.
func TestOpenAPIDescribesTheGateway(t *testing.T) {
	resp, err := openapi.Generate(openapi.RequestFor("", testv1.File_platformtest_v1_echo_proto))
	if err != nil {
		t.Fatal(err)
	}
	doc := resp[openapi.BundleFile]
	if err := oascheck.Validate(doc); err != nil {
		t.Fatalf("%v\n%s", err, doc)
	}
	check, err := oascheck.NewChecker(doc)
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		Paths map[string]map[string]struct {
			RequestBody struct {
				Required bool `json:"required"`
				Content  map[string]struct {
					Schema struct {
						Ref string `json:"$ref"`
					} `json:"schema"`
				} `json:"content"`
			} `json:"requestBody"`
			Responses map[string]struct {
				Content map[string]struct {
					Schema struct {
						Ref string `json:"$ref"`
					} `json:"schema"`
				} `json:"content"`
			} `json:"responses"`
		} `json:"paths"`
	}
	v, err := oascheck.JSON(doc)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(v)
	if err := json.Unmarshal(raw, &spec); err != nil {
		t.Fatal(err)
	}
	// schemaOf is the component the description names for the answer of an operation.
	schemaOf := func(verb, path string, code int) string {
		if code != http.StatusOK {
			return "Error"
		}
		ref := spec.Paths[path][verb].Responses["200"].Content["application/json"].Schema.Ref
		if ref == "" {
			t.Fatalf("%s %s has no 200 schema", verb, path)
		}
		return strings.TrimPrefix(ref, "#/components/schemas/")
	}

	// The check itself is strict: a number where JSON writes a string, a field the
	// gateway always writes left out, a field the description does not know.
	for _, bad := range []string{
		`{"id":7}`,
		`{"message_text":"a","length":1}`,
		`{"message_text":"a","length":1,"shouted":false,"extra":1}`,
	} {
		name := "platformtest.v1.KindsResponse"
		if strings.Contains(bad, "message_text") {
			name = "platformtest.v1.EchoResponse"
		}
		if err := check.Check(name, []byte(bad)); err == nil {
			t.Errorf("%s matches %s", bad, name)
		}
	}

	s := start(t, api.Config{})
	body, contentType := multipartBody(t, func(w *multipart.Writer) {
		_ = w.WriteField("title", "Menu")
		filePart(t, w, "avatar", "logo.png", "image/png", "PNG")
		filePart(t, w, "images", "a.png", "image/png", "A")
		_ = w.WriteField("meta", `{"author":"aidar"}`)
	})
	calls := []struct {
		verb, path, template, body, contentType string
		code                                    int
		holds                                   string
	}{
		{"get", "/v1/kinds/7", "/v1/kinds/{id}", "", "", http.StatusOK, `"id":"7"`},
		{"get", "/v1/kinds/9007199254740993?full=true&color=COLOR_RED&tags=a&tags=b", "/v1/kinds/{id}", "", "", http.StatusOK, `"9007199254740993"`},
		{"post", "/v1/echo", "/v1/echo", `{"message_text":"hi"}`, "application/json", http.StatusOK, `"length":2`},
		{"post", "/v1/old-echo", "/v1/old-echo", `{"messageText":"hi"}`, "application/json", http.StatusOK, `"hi"`},
		{"post", "/v1/echo", "/v1/echo", `{"message_text":""}`, "application/json", http.StatusBadRequest, "buf.validate.Violations"},
		{"get", "/v1/fail/not_found", "/v1/fail/{kind}", "", "", http.StatusNotFound, "no such order"},
		{"get", "/v1/fail/external", "/v1/fail/{kind}", "", "", http.StatusBadGateway, "EXTERNAL_UNAVAILABLE"},
		{"get", "/v1/fail/details", "/v1/fail/{kind}", "", "", http.StatusBadRequest, "google.rpc.BadRequest"},
		{"get", "/v1/fail/ok", "/v1/fail/{kind}", "", "", http.StatusOK, "{}"},
		{"post", "/v1/upload", "/v1/upload", body.String(), contentType, http.StatusOK, `"avatar_size":3`},
		{"post", "/v1/upload", "/v1/upload", `{"title":"t","avatar":{"filename":"a","content":"AAE="}}`, "application/json", http.StatusOK, `"avatar_size":2`},
		{"post", "/v1/things", "/v1/things", `{"name":"a"}`, "application/json", http.StatusOK, `"calls"`},
		// The gateway reads an empty body as {}: validation decides, as for {}.
		{"post", "/v1/echo", "/v1/echo", "", "application/json", http.StatusBadRequest, "buf.validate.Violations"},
		{"post", "/v1/things", "/v1/things", "", "application/json", http.StatusOK, `"calls"`},
	}
	// Every body of the test service has fields, so the description asks for it: a
	// client can always send {}, and an empty body means the same to the gateway.
	for path, item := range spec.Paths {
		for verb, op := range item {
			if len(op.RequestBody.Content) > 0 && !op.RequestBody.Required {
				t.Errorf("%s %s: the request body is not required", verb, path)
			}
		}
	}
	for _, c := range calls {
		header := map[string]string{}
		if c.contentType != "" {
			header["Content-Type"] = c.contentType
		}
		code, got, _ := s.do(strings.ToUpper(c.verb), c.path, c.body, header)
		if code != c.code {
			t.Errorf("%s %s: code = %d, want %d\n%s", c.verb, c.path, code, c.code, got)
			continue
		}
		if !strings.Contains(strings.ReplaceAll(got, " ", ""), strings.ReplaceAll(c.holds, " ", "")) {
			t.Errorf("%s %s: the answer lacks %s\n%s", c.verb, c.path, c.holds, got)
		}
		if err := check.Check(schemaOf(c.verb, c.template, code), []byte(got)); err != nil {
			t.Errorf("%s %s: %v", c.verb, c.path, err)
		}
		// A JSON request the gateway took matches the request schema too.
		if ref := spec.Paths[c.template][c.verb].RequestBody.Content["application/json"].Schema.Ref; ref != "" && code == http.StatusOK && c.contentType == "application/json" && c.body != "" {
			if err := check.Check(strings.TrimPrefix(ref, "#/components/schemas/"), []byte(c.body)); err != nil {
				t.Errorf("%s %s request: %v", c.verb, c.path, err)
			}
		}
	}
}
