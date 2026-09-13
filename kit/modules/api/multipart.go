package api

// File uploads through the REST gateway, ported from taply.
//
// A multipart/form-data request fills the proto request message: a part whose name is a
// file field sets a message with filename, content_type and content fields; a repeated
// file field takes several parts in order (images, images) or by index (images[0],
// images[2]), with empty messages for skipped indices; a part holding JSON fills a
// message field; any other part sets a scalar field.
//
//	message File {
//	  string filename = 1;
//	  string content_type = 2;
//	  bytes content = 3;
//	}
//
//	curl -F title=Menu -F avatar=@logo.png -F images=@a.png -F images=@b.png -F 'meta={"author":"aidar"}'

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"github.com/grpc-ecosystem/grpc-gateway/v2/utilities"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// maxFileIndex bounds images[N], so a request cannot make the server allocate a huge
// list of empty files.
const maxFileIndex = 1000

type fileUpload struct {
	filename    string
	contentType string
	content     []byte
}

type indexedUpload struct {
	index int
	file  fileUpload
}

var indexedField = regexp.MustCompile(`^(.+)\[(\d+)\]$`)

// multipartMarshaler reads multipart/form-data requests and answers in JSON.
type multipartMarshaler struct {
	runtime.Marshaler
}

var _ runtime.Marshaler = (*multipartMarshaler)(nil)

func newMultipartMarshaler(json runtime.Marshaler) *multipartMarshaler {
	return &multipartMarshaler{Marshaler: json}
}

// ContentType of the answer: the request is multipart, the response is JSON.
func (m *multipartMarshaler) ContentType(any) string { return "application/json" }

// Unmarshal reads a whole body.
func (m *multipartMarshaler) Unmarshal(data []byte, v any) error {
	return m.NewDecoder(bytes.NewReader(data)).Decode(v)
}

// NewDecoder reads the body. The gateway does not pass the Content-Type header to a
// decoder, so the boundary is taken from the first line of the body, as taply does.
func (m *multipartMarshaler) NewDecoder(r io.Reader) runtime.Decoder {
	return runtime.DecoderFunc(func(v any) error {
		msg, ok := v.(proto.Message)
		if !ok {
			return fmt.Errorf("multipart: target is not a proto message, got %T", v)
		}

		br := bufio.NewReaderSize(r, 4096)
		head, err := br.Peek(512)
		if err != nil && !errors.Is(err, io.EOF) {
			return fmt.Errorf("multipart: read boundary: %w", err)
		}
		firstLine, _, _ := bytes.Cut(head, []byte("\n"))
		boundary := strings.TrimSuffix(strings.TrimSpace(string(bytes.TrimPrefix(firstLine, []byte("--")))), "\r")
		if boundary == "" || !bytes.HasPrefix(firstLine, []byte("--")) {
			return errors.New("multipart: no boundary at the start of the body")
		}

		values := url.Values{}
		repeated := map[string][]indexedUpload{}
		single := map[string]fileUpload{}
		sequence := map[string]int{}

		mr := multipart.NewReader(br, boundary)
		for {
			part, err := mr.NextPart()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return fmt.Errorf("multipart: read part: %w", err)
			}
			name := part.FormName()
			if name == "" {
				continue
			}
			data, err := io.ReadAll(part)
			if err != nil {
				return fmt.Errorf("multipart: read part %s: %w", name, err)
			}

			index := -1
			base := ""
			if match := indexedField.FindStringSubmatch(name); match != nil {
				base = match[1]
				if n, err := strconv.Atoi(match[2]); err == nil {
					index = n
				}
			}

			field, err := fieldByPath(msg, name)
			if err != nil && base != "" {
				if field, err = fieldByPath(msg, base); err == nil {
					name = base
				}
			}
			if err != nil {
				continue // a part the message has no field for is ignored, like unknown JSON fields
			}

			switch {
			case field.Kind() == protoreflect.BytesKind:
				values.Set(name, base64.StdEncoding.EncodeToString(data))

			case part.FileName() != "" && field.Kind() == protoreflect.MessageKind:
				upload := fileUpload{filename: part.FileName(), contentType: part.Header.Get("Content-Type"), content: data}
				if !field.IsList() {
					single[name] = upload
					continue
				}
				i := index
				if i < 0 {
					i = sequence[name]
					sequence[name]++
				}
				repeated[name] = append(repeated[name], indexedUpload{index: i, file: upload})

			case field.Kind() == protoreflect.MessageKind && !field.IsList():
				sub := msg.ProtoReflect().Mutable(field).Message().Interface()
				if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(data, sub); err != nil {
					return fmt.Errorf("multipart: field %s: %w", name, err)
				}

			case field.Kind() == protoreflect.MessageKind:
				// A repeated message without a file: an empty slot, nothing to set.

			default:
				values.Add(name, string(data))
			}
		}

		for name, upload := range single {
			values.Set(name+".filename", upload.filename)
			values.Set(name+".content_type", upload.contentType)
			values.Set(name+".content", base64.StdEncoding.EncodeToString(upload.content))
		}
		if err := runtime.PopulateQueryParameters(msg, values, &utilities.DoubleArray{}); err != nil {
			return fmt.Errorf("multipart: %w", err)
		}
		return setRepeatedFiles(msg, repeated)
	})
}

func fieldByPath(msg proto.Message, path string) (protoreflect.FieldDescriptor, error) {
	current := msg.ProtoReflect()
	parts := strings.Split(path, ".")
	var field protoreflect.FieldDescriptor
	for i, name := range parts {
		fields := current.Descriptor().Fields()
		field = fields.ByName(protoreflect.Name(name))
		if field == nil {
			field = fields.ByJSONName(name)
		}
		if i == len(parts)-1 {
			break
		}
		if field == nil || field.Message() == nil || field.IsList() {
			return nil, fmt.Errorf("multipart: no field %q", path)
		}
		current = current.Mutable(field).Message()
	}
	if field == nil {
		return nil, fmt.Errorf("multipart: no field %q", path)
	}
	return field, nil
}

func setRepeatedFiles(msg proto.Message, uploads map[string][]indexedUpload) error {
	m := msg.ProtoReflect()
	for name, files := range uploads {
		field := m.Descriptor().Fields().ByName(protoreflect.Name(name))
		if field == nil {
			field = m.Descriptor().Fields().ByJSONName(name)
		}
		if field == nil || !field.IsList() || field.Message() == nil {
			return fmt.Errorf("multipart: %s is not a repeated file field", name)
		}

		last := -1
		for _, f := range files {
			last = max(last, f.index)
		}
		if last >= maxFileIndex {
			return fmt.Errorf("multipart: %s[%d]: the index is above %d", name, last, maxFileIndex)
		}
		slots := make([]*fileUpload, last+1)
		for i := range files {
			slots[files[i].index] = &files[i].file
		}

		list := m.Mutable(field).List()
		elem := field.Message().Fields()
		for _, f := range slots {
			item := list.NewElement()
			if f != nil {
				if fd := elem.ByName("filename"); fd != nil {
					item.Message().Set(fd, protoreflect.ValueOfString(f.filename))
				}
				if fd := elem.ByName("content_type"); fd != nil {
					item.Message().Set(fd, protoreflect.ValueOfString(f.contentType))
				}
				if fd := elem.ByName("content"); fd != nil {
					item.Message().Set(fd, protoreflect.ValueOfBytes(f.content))
				}
			}
			list.Append(item)
		}
	}
	return nil
}
