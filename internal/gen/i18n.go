package gen

import "github.com/aidarbn/platform-go/internal/spec"

// Files of the i18n module.
const (
	MessagesPath     = "i18n/messages.yaml"
	MessagesGoPath   = "i18n/messages.gen.go"
	I18nProtoPath    = "proto/platform/i18n/v1/i18n.proto"
	platformProtoDir = "proto/platform"
)

const messagesGo = header + `
// Package i18n holds the static translations of the project, i18n/messages.yaml,
// embedded into the binary.
package i18n

import _ "embed"

// Messages is i18n/messages.yaml.
//
//go:embed messages.yaml
var Messages string
`

// MessagesExample is the file a project starts from when it enables the i18n module.
const MessagesExample = `# Static translations of the project: error messages and other texts, in taply's format.
# The platform translates its own messages and taply's generic ones already.
#
#   error.msg.<exact message>        an exact message
#   error.resource.<name>            the resource of "product 42: not found"
#   error.field.<name>               the field of "missing name" and "name is required"
#   error.tmpl.<key>                 a sentence with %s slots; en is the pattern it matches
#
# error.msg.product is out of stock:
#   en: product is out of stock
#   ru: товар не в наличии
#   kk: өнім қоймада жоқ

error.resource.order:
  en: order
  ru: заказ
  kk: тапсырыс
`

// I18nProto is the field option of the platform, the same file kit/i18nx is generated
// from. A test keeps the two identical.
const I18nProto = `syntax = "proto3";

// Translatable fields of API messages, ported from taply.
package platform.i18n.v1;

import "google/protobuf/descriptor.proto";

option go_package = "github.com/aidarbn/platform-go/kit/i18nx/i18npb;i18npb";

// I18nFieldOptions marks a string field as translatable.
//
// key is "<entity>.<field>": the entity type before the dot, the translated field after
// it. It is the entity and field columns of the i18n_translations table and the key of a
// field_translations map.
//
// instance_key is the name of the sibling field that holds the id of the entity, such as
// "id". Without it the translation is shared by every instance.
//
// For every response the i18n interceptor walks the message tree, reads the ids of the
// annotated fields, loads their translations in one query per entity type and replaces
// the values with the translation for the Accept-Language of the request. A message with
// a field_translations map is left alone: it belongs to an editor, which reads and writes
// every locale itself.
//
//   message Product {
//     int64 id = 1;
//     string name = 2 [(platform.i18n.v1.i18n_field) = {key: "product.name", instance_key: "id"}];
//     map<string, platform.i18n.v1.LocaleMap> field_translations = 10;
//   }
message I18nFieldOptions {
  string key = 1;
  string instance_key = 2;
}

extend google.protobuf.FieldOptions {
  I18nFieldOptions i18n_field = 50001;
}

// LocaleMap holds the translations of one field: locale to text.
message LocaleMap {
  map<string, string> values = 1;
}
`

func i18nFiles(f *spec.File) map[string][]byte {
	files := map[string][]byte{MessagesGoPath: []byte(messagesGo)}
	if _, ok := f.Modules["api"]; ok {
		files[I18nProtoPath] = []byte(I18nProto)
	}
	return files
}
