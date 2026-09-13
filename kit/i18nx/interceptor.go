package i18nx

import (
	"context"
	"fmt"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/aidarbn/platform-go/kit/i18nx/i18npb"
)

// ResponseInterceptor translates responses and error messages by the locale of the
// request. skip, when set, turns substitution off for a call — taply does that for
// tablets of a restaurant with translations disabled; errors are translated anyway.
func (c *Client) ResponseInterceptor(skip func(ctx context.Context) bool) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		resp, err := handler(ctx, req)
		locale := c.ResolveLocale(ctx)
		if err != nil {
			return resp, c.TranslateError(err, locale)
		}
		if skip != nil && skip(ctx) {
			return resp, nil
		}
		msg, ok := resp.(proto.Message)
		if !ok || c.pool == nil {
			return resp, nil
		}

		refs := collectRefs(msg.ProtoReflect())
		if len(refs) == 0 {
			return resp, nil
		}
		cache := c.load(ctx, refs)
		if len(cache) > 0 {
			substitute(msg.ProtoReflect(), cache, locale)
		}
		return resp, nil
	}
}

type entityRef struct{ entity, id string }

type translationCache map[string]Translations

func cacheKey(entity, id string) string { return entity + ":" + id }

// hasFieldTranslations tells an editor's message, which carries every locale and is
// managed by its handler, from a message shown to users.
func hasFieldTranslations(md protoreflect.MessageDescriptor) bool {
	f := md.Fields().ByName("field_translations")
	return f != nil && f.IsMap()
}

func collectRefs(msg protoreflect.Message) []entityRef {
	seen := map[string]bool{}
	var refs []entityRef
	walk(msg, func(m protoreflect.Message, fd protoreflect.FieldDescriptor, opts *i18npb.I18NFieldOptions) {
		entity, _, ok := splitKey(opts.GetKey())
		if !ok {
			return
		}
		id, ok := instanceID(m, opts)
		if !ok {
			return
		}
		if k := cacheKey(entity, id); !seen[k] {
			seen[k] = true
			refs = append(refs, entityRef{entity: entity, id: id})
		}
	})
	return refs
}

// load reads the translations with one query per entity type.
func (c *Client) load(ctx context.Context, refs []entityRef) translationCache {
	byEntity := map[string][]string{}
	for _, r := range refs {
		byEntity[r.entity] = append(byEntity[r.entity], r.id)
	}
	cache := translationCache{}
	for entity, ids := range byEntity {
		found, err := c.GetBatchAll(ctx, entity, ids)
		if err != nil {
			continue // an untranslated answer is better than a failed one
		}
		for id, t := range found {
			cache[cacheKey(entity, id)] = t
		}
	}
	return cache
}

func substitute(msg protoreflect.Message, cache translationCache, locale Locale) {
	walk(msg, func(m protoreflect.Message, fd protoreflect.FieldDescriptor, opts *i18npb.I18NFieldOptions) {
		entity, field, ok := splitKey(opts.GetKey())
		if !ok || fd.Kind() != protoreflect.StringKind {
			return
		}
		id, ok := instanceID(m, opts)
		if !ok {
			return
		}
		if text := cache[cacheKey(entity, id)][field][locale]; text != "" {
			m.Set(fd, protoreflect.ValueOfString(text))
		}
	})
}

// walk visits every annotated field of a message tree, skipping editor messages.
func walk(msg protoreflect.Message, visit func(protoreflect.Message, protoreflect.FieldDescriptor, *i18npb.I18NFieldOptions)) {
	md := msg.Descriptor()
	if hasFieldTranslations(md) {
		return
	}
	fields := md.Fields()
	for i := range fields.Len() {
		fd := fields.Get(i)
		if opts := fieldOptions(fd); opts != nil {
			visit(msg, fd, opts)
			continue
		}
		if fd.Kind() != protoreflect.MessageKind || fd.IsMap() {
			continue
		}
		if fd.IsList() {
			list := msg.Get(fd).List()
			for j := range list.Len() {
				walk(list.Get(j).Message(), visit)
			}
		} else if msg.Has(fd) {
			walk(msg.Get(fd).Message(), visit)
		}
	}
}

// instanceID reads the id of the entity; a shared translation has none.
func instanceID(msg protoreflect.Message, opts *i18npb.I18NFieldOptions) (string, bool) {
	name := opts.GetInstanceKey()
	if name == "" {
		return "", true
	}
	fd := msg.Descriptor().Fields().ByName(protoreflect.Name(name))
	if fd == nil {
		return "", false
	}
	v := msg.Get(fd)
	var id string
	switch fd.Kind() {
	case protoreflect.StringKind:
		id = v.String()
	case protoreflect.Int32Kind, protoreflect.Int64Kind, protoreflect.Sint32Kind, protoreflect.Sint64Kind:
		id = fmt.Sprint(v.Int())
	case protoreflect.Uint32Kind, protoreflect.Uint64Kind:
		id = fmt.Sprint(v.Uint())
	}
	return id, id != "" && id != "0"
}

func splitKey(key string) (entity, field string, ok bool) {
	entity, field, ok = strings.Cut(key, ".")
	return entity, field, ok && entity != "" && field != ""
}

func fieldOptions(fd protoreflect.FieldDescriptor) *i18npb.I18NFieldOptions {
	opts := fd.Options()
	if opts == nil {
		return nil
	}
	v, _ := proto.GetExtension(opts, i18npb.E_I18NField).(*i18npb.I18NFieldOptions)
	return v
}

// TranslateError translates the message of a status error. Internal, unavailable,
// unknown and unimplemented errors keep their text: they are for developers.
func (c *Client) TranslateError(err error, locale Locale) error {
	st, ok := status.FromError(err)
	if !ok {
		return err
	}
	switch st.Code() {
	case codes.Internal, codes.Unavailable, codes.Unknown, codes.Unimplemented:
		return err
	}
	translated := c.TranslateMessage(st.Message(), locale)
	if translated == st.Message() {
		return err
	}
	p := st.Proto()
	p.Message = translated
	return status.FromProto(p).Err()
}

// TranslateMessage translates a message the way taply does, in order: a whole sentence
// template, "{resource} {id}: {message}", a "missing {field}" or "{field} is required"
// pattern, an exact message. An untranslated message comes back unchanged.
func (c *Client) TranslateMessage(msg string, locale Locale) string {
	if translated, ok := c.translateTemplate(msg, locale); ok {
		return translated
	}
	if prefix, message, ok := strings.Cut(msg, ": "); ok {
		if resource, id, ok := strings.Cut(prefix, " "); ok {
			trResource := c.staticOr(keyResource+resource, resource, locale)
			trMessage := c.translatePlain(message, locale)
			if trResource != resource || trMessage != message {
				return trResource + " " + id + ": " + trMessage
			}
			return msg
		}
	}
	return c.translatePlain(msg, locale)
}

var errorPatterns = []struct{ prefix, suffix, key string }{
	{suffix: " is required", key: keyPattern + "is_required"},
	{prefix: "missing ", key: keyPattern + "missing"},
}

func (c *Client) translatePlain(msg string, locale Locale) string {
	for _, p := range errorPatterns {
		var field string
		switch {
		case p.suffix != "" && strings.HasSuffix(msg, p.suffix):
			field = strings.TrimSuffix(msg, p.suffix)
		case p.prefix != "" && strings.HasPrefix(msg, p.prefix):
			field = strings.TrimPrefix(msg, p.prefix)
		default:
			continue
		}
		tmpl := c.Static(p.key, locale)
		if tmpl == p.key {
			continue
		}
		return fmt.Sprintf(tmpl, c.staticOr(keyField+field, field, locale))
	}
	return c.staticOr(keyMessage+msg, msg, locale)
}

func (c *Client) translateTemplate(msg string, locale Locale) (string, bool) {
	c.mu.RLock()
	templates := append([]errorTemplate(nil), c.templates...)
	c.mu.RUnlock()
	for _, t := range templates {
		args := matchTemplate(t.en, msg)
		if args == nil {
			continue
		}
		return fmt.Sprintf(c.Static(t.key, locale), args...), true
	}
	return msg, false
}

// matchTemplate captures the %s slots of a skeleton from a message, or returns nil.
func matchTemplate(skeleton, msg string) []any {
	segments := strings.Split(skeleton, "%s")
	if len(segments) < 2 || !strings.HasPrefix(msg, segments[0]) {
		return nil
	}
	rest := msg[len(segments[0]):]
	args := make([]any, 0, len(segments)-1)
	for _, sep := range segments[1:] {
		if sep == "" {
			args = append(args, rest)
			rest = ""
			continue
		}
		i := strings.Index(rest, sep)
		if i < 0 {
			return nil
		}
		args = append(args, rest[:i])
		rest = rest[i+len(sep):]
	}
	if rest != "" {
		return nil
	}
	return args
}

func (c *Client) staticOr(key, original string, locale Locale) string {
	if text := c.Static(key, locale); text != key {
		return text
	}
	return original
}

// RequestInterceptor copies the base value of every annotated field of an editor's
// request into its field_translations map under the default locale, so the base column
// and the default translation never disagree. Taken from taply.
func (c *Client) RequestInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if msg, ok := req.(proto.Message); ok {
			mirror(msg.ProtoReflect(), c.defaultLocale)
		}
		return handler(ctx, req)
	}
}

func mirror(msg protoreflect.Message, locale Locale) {
	md := msg.Descriptor()
	ft := md.Fields().ByName("field_translations")
	hasFT := ft != nil && ft.IsMap()

	fields := md.Fields()
	for i := range fields.Len() {
		fd := fields.Get(i)
		if hasFT {
			if opts := fieldOptions(fd); opts != nil && fd.Kind() == protoreflect.StringKind {
				if _, field, ok := splitKey(opts.GetKey()); ok {
					if base := strings.TrimSpace(msg.Get(fd).String()); base != "" {
						entry := msg.Mutable(ft).Map().Mutable(protoreflect.ValueOfString(field).MapKey()).Message()
						if values := entry.Descriptor().Fields().ByName("values"); values != nil && values.IsMap() {
							entry.Mutable(values).Map().Set(protoreflect.ValueOfString(string(locale)).MapKey(), protoreflect.ValueOfString(base))
						}
					}
				}
				continue
			}
		}
		if fd.Kind() != protoreflect.MessageKind || fd.IsMap() {
			continue
		}
		if fd.IsList() {
			list := msg.Get(fd).List()
			for j := range list.Len() {
				mirror(list.Get(j).Message(), locale)
			}
		} else if msg.Has(fd) {
			mirror(msg.Mutable(fd).Message(), locale)
		}
	}
}
