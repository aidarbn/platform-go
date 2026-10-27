package i18nx

import (
	"context"
	"fmt"
	"strings"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"github.com/aidarbn/platform-go/kit/i18nx/i18npb"
)

// ResponseInterceptor translates responses and error messages by the locale of the
// request. skip, when set, turns substitution off for a call — taply does that for
// tablets of a restaurant with translations disabled; errors are translated anyway. It is
// ErrorInterceptor and FieldsInterceptor in one; the i18n module puts the two in separate
// places of the chain.
func (c *Client) ResponseInterceptor(skip func(ctx context.Context) bool) grpc.UnaryServerInterceptor {
	errs, fields := c.ErrorInterceptor(), c.FieldsInterceptor(skip)
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		return errs(ctx, req, info, func(ctx context.Context, req any) (any, error) {
			return fields(ctx, req, info, handler)
		})
	}
}

// ErrorInterceptor translates the message of an error by the locale of the request. It
// belongs outside the project's authentication, so its refusals are translated too.
func (c *Client) ErrorInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		resp, err := handler(ctx, req)
		if err != nil {
			return resp, c.TranslateError(err, c.ResolveLocale(ctx))
		}
		return resp, nil
	}
}

// FieldsInterceptor puts the translations of annotated fields into a response by the
// locale of the request. skip, when set, leaves a call untranslated; it belongs after
// the project's authentication, so skip sees the caller.
func (c *Client) FieldsInterceptor(skip func(ctx context.Context) bool) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		resp, err := handler(ctx, req)
		if err != nil {
			return resp, err
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
			substitute(msg.ProtoReflect(), cache, c.ResolveLocale(ctx))
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
// unknown and unimplemented errors keep their text: they are for developers; so do the
// failures of external providers (an ErrorInfo of reason EXTERNAL_UNAVAILABLE or
// EXTERNAL_TIMEOUT, as api.ExternalUnavailable and api.ExternalTimeout build them),
// whose text names the provider.
func (c *Client) TranslateError(err error, locale Locale) error {
	st, ok := status.FromError(err)
	if !ok {
		return err
	}
	switch st.Code() {
	case codes.Internal, codes.Unavailable, codes.Unknown, codes.Unimplemented:
		return err
	}
	for _, d := range st.Details() {
		if info, ok := d.(*errdetails.ErrorInfo); ok && (info.GetReason() == "EXTERNAL_UNAVAILABLE" || info.GetReason() == "EXTERNAL_TIMEOUT") {
			return err
		}
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
// and the default translation never disagree. Taken from taply. The i18n module runs it
// after validation, so the translation gets the value the checks passed.
func (c *Client) RequestInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if msg, ok := req.(proto.Message); ok {
			mirror(msg.ProtoReflect(), c.defaultLocale)
		}
		return handler(ctx, req)
	}
}

// BaseFromTranslationInterceptor fills the base field of an editor's request from its
// translation into the default locale, taken from taply: when the request carries the
// key of the default locale for a field — empty too, which clears it — the base field
// takes its trimmed value; without the key the base field stays as sent and
// RequestInterceptor copies it into the translation. An editor that edits only the
// translations sends the base field as it loaded it, and without this the old base
// value would overwrite the new translation. It belongs before string normalisation and
// validation, so a base value from a translation passes the same checks as one sent
// directly.
func (c *Client) BaseFromTranslationInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if msg, ok := req.(proto.Message); ok {
			baseFromTranslation(msg.ProtoReflect(), c.defaultLocale)
		}
		return handler(ctx, req)
	}
}

// mirror writes the trimmed base value of every annotated field into the default
// locale of its translations; an empty base value is skipped.
func mirror(msg protoreflect.Message, locale Locale) {
	walkEditorFields(msg, func(m protoreflect.Message, fd, ft protoreflect.FieldDescriptor, field string) {
		if base := strings.TrimSpace(m.Get(fd).String()); base != "" {
			setTranslation(m, ft, field, locale, base)
		}
	})
}

// baseFromTranslation sets every annotated field whose translation into the default
// locale was sent to that translation, trimmed, and writes the trimmed value back, so a
// translation of blanks is saved as empty rather than skipped by mirror.
func baseFromTranslation(msg protoreflect.Message, locale Locale) {
	walkEditorFields(msg, func(m protoreflect.Message, fd, ft protoreflect.FieldDescriptor, field string) {
		text, ok := translation(m, ft, field, locale)
		if !ok {
			return
		}
		text = strings.TrimSpace(text)
		m.Set(fd, protoreflect.ValueOfString(text))
		setTranslation(m, ft, field, locale, text)
	})
}

// walkEditorFields calls fn for every annotated string field of a message that has a
// field_translations map, through nested messages and lists of them.
func walkEditorFields(msg protoreflect.Message, fn func(m protoreflect.Message, fd, ft protoreflect.FieldDescriptor, field string)) {
	md := msg.Descriptor()
	ft := md.Fields().ByName("field_translations")
	hasFT := ft != nil && ft.IsMap()

	fields := md.Fields()
	for i := range fields.Len() {
		fd := fields.Get(i)
		if hasFT {
			if opts := fieldOptions(fd); opts != nil && fd.Kind() == protoreflect.StringKind {
				if _, field, ok := splitKey(opts.GetKey()); ok {
					fn(msg, fd, ft, field)
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
				walkEditorFields(list.Get(j).Message(), fn)
			}
		} else if msg.Has(fd) {
			walkEditorFields(msg.Mutable(fd).Message(), fn)
		}
	}
}

// translation reads field_translations[field].values[locale]; ok is false when the
// field or the locale is not in the request.
func translation(msg protoreflect.Message, ft protoreflect.FieldDescriptor, field string, locale Locale) (string, bool) {
	if !msg.Has(ft) {
		return "", false
	}
	key := protoreflect.ValueOfString(field).MapKey()
	entries := msg.Get(ft).Map()
	if !entries.Has(key) {
		return "", false
	}
	entry := entries.Get(key).Message()
	values := entry.Descriptor().Fields().ByName("values")
	if values == nil || !values.IsMap() {
		return "", false
	}
	byLocale := entry.Get(values).Map()
	lkey := protoreflect.ValueOfString(string(locale)).MapKey()
	if !byLocale.Has(lkey) {
		return "", false
	}
	return byLocale.Get(lkey).String(), true
}

// setTranslation writes field_translations[field].values[locale], keeping the other
// locales.
func setTranslation(msg protoreflect.Message, ft protoreflect.FieldDescriptor, field string, locale Locale, text string) {
	entry := msg.Mutable(ft).Map().Mutable(protoreflect.ValueOfString(field).MapKey()).Message()
	if values := entry.Descriptor().Fields().ByName("values"); values != nil && values.IsMap() {
		entry.Mutable(values).Map().Set(protoreflect.ValueOfString(string(locale)).MapKey(), protoreflect.ValueOfString(text))
	}
}
