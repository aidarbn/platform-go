package openapi

import (
	"encoding/json"
	"slices"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"

	"github.com/aidarbn/platform-go/kit/openapipb"
)

// The platform's own options, platform/openapi/v1/options.proto.

func serviceOptions(sd protoreflect.ServiceDescriptor) *openapipb.ServiceOptions {
	opts, ok := sd.Options().(*descriptorpb.ServiceOptions)
	if !ok || opts == nil || !proto.HasExtension(opts, openapipb.E_Service) {
		return nil
	}
	o, _ := proto.GetExtension(opts, openapipb.E_Service).(*openapipb.ServiceOptions)
	return o
}

func methodOptions(m protoreflect.MethodDescriptor) *openapipb.MethodOptions {
	opts, ok := m.Options().(*descriptorpb.MethodOptions)
	if !ok || opts == nil || !proto.HasExtension(opts, openapipb.E_Method) {
		return nil
	}
	o, _ := proto.GetExtension(opts, openapipb.E_Method).(*openapipb.MethodOptions)
	return o
}

// fieldExample is the example of a field: its JSON, or the text itself when it is not
// JSON, so a forgotten pair of quotes still shows.
func fieldExample(fd protoreflect.FieldDescriptor) []any {
	opts, ok := fd.Options().(*descriptorpb.FieldOptions)
	if !ok || opts == nil || !proto.HasExtension(opts, openapipb.E_Field) {
		return nil
	}
	o, _ := proto.GetExtension(opts, openapipb.E_Field).(*openapipb.FieldOptions)
	return example(o.GetExample())
}

func messageExample(md protoreflect.MessageDescriptor) []any {
	opts, ok := md.Options().(*descriptorpb.MessageOptions)
	if !ok || opts == nil || !proto.HasExtension(opts, openapipb.E_Message) {
		return nil
	}
	o, _ := proto.GetExtension(opts, openapipb.E_Message).(*openapipb.MessageOptions)
	return example(o.GetExample())
}

func example(text string) []any {
	if text == "" {
		return nil
	}
	var v any
	if err := json.Unmarshal([]byte(text), &v); err != nil {
		return []any{text}
	}
	return []any{v}
}

// tagSet collects the tags of the operations in the order they first appear, with
// their descriptions and groups.
type tagSet struct {
	list  []*Tag
	group map[string]string
	order []string // groups in the order they first appear
}

func (t *tagSet) add(name, description, group string) {
	if t.group == nil {
		t.group = map[string]string{}
	}
	i := slices.IndexFunc(t.list, func(x *Tag) bool { return x.Name == name })
	if i < 0 {
		t.list = append(t.list, &Tag{Name: name, Description: description})
	} else if t.list[i].Description == "" {
		t.list[i].Description = description
	}
	if group != "" && t.group[name] == "" {
		t.group[name] = group
		if !slices.Contains(t.order, group) {
			t.order = append(t.order, group)
		}
	}
}

// tagGroup is an entry of x-tagGroups.
type tagGroup struct {
	Name string   `yaml:"name"`
	Tags []string `yaml:"tags"`
}

// groups is x-tagGroups, nil without groups. Scalar and Redoc hide a tag that is in no
// group, so a tag without one gets a group of its own name.
func (t *tagSet) groups() []tagGroup {
	if len(t.order) == 0 {
		return nil
	}
	var out []tagGroup
	for _, g := range t.order {
		entry := tagGroup{Name: g}
		for _, tag := range t.list {
			if t.group[tag.Name] == g {
				entry.Tags = append(entry.Tags, tag.Name)
			}
		}
		out = append(out, entry)
	}
	for _, tag := range t.list {
		if t.group[tag.Name] == "" {
			out = append(out, tagGroup{Name: tag.Name, Tags: []string{tag.Name}})
		}
	}
	return out
}
