package convert

import (
	"bytes"
	"slices"
	"strings"

	gwopts "github.com/grpc-ecosystem/grpc-gateway/v2/protoc-gen-openapiv2/options"
	"go.yaml.in/yaml/v3"
	"google.golang.org/protobuf/proto"
)

type fileSwagger struct {
	path    string
	swagger *gwopts.Swagger
}

// baseYAML is api/openapi/base.yaml from the openapiv2_swagger options: the info of the
// first file that has one, the security schemes, tags and x-tagGroups of all of them,
// the first ones winning.
func baseYAML(files []fileSwagger, r *Report) ([]byte, error) {
	root := &yaml.Node{Kind: yaml.MappingNode}
	add := func(m *yaml.Node, k string, v *yaml.Node) {
		m.Content = append(m.Content, str(k), v)
	}
	add(root, "openapi", str("3.1.0"))

	var info *gwopts.Info
	var docs *gwopts.ExternalDocumentation
	var security []*gwopts.SecurityRequirement
	var tags []*gwopts.Tag
	var groups any
	schemes := map[string]*gwopts.SecurityScheme{}
	var schemeNames []string
	var sources []string
	for _, f := range files {
		sw := f.swagger
		sources = append(sources, f.path)
		switch {
		case info == nil && sw.GetInfo() != nil:
			info = sw.GetInfo()
			if len(info.GetExtensions()) > 0 {
				r.note(f.path, 0, true, "the extensions of info are left out: %s; add them to the base by hand", strings.Join(sortedKeys(info.GetExtensions()), ", "))
			}
		case sw.GetInfo() != nil:
			r.note(f.path, 0, false, "info %q is left out: the base takes the info of %s", sw.GetInfo().GetTitle(), sources[0])
		}
		switch {
		case docs == nil && sw.GetExternalDocs() != nil:
			docs = sw.GetExternalDocs()
		case sw.GetExternalDocs() != nil && !proto.Equal(docs, sw.GetExternalDocs()):
			r.note(f.path, 0, true, "external_docs %s is left out: the base takes the one of an earlier file", sw.GetExternalDocs().GetUrl())
		}
		switch {
		case security == nil:
			security = sw.GetSecurity()
		case len(sw.GetSecurity()) > 0 && !slices.EqualFunc(security, sw.GetSecurity(), func(a, b *gwopts.SecurityRequirement) bool { return proto.Equal(a, b) }):
			r.note(f.path, 0, true, "its security requirements differ from the ones of an earlier file and are left out; OpenAPI has one list for the API, and a method needs its own")
		}
		for _, t := range sw.GetTags() {
			i := slices.IndexFunc(tags, func(x *gwopts.Tag) bool { return x.GetName() == t.GetName() })
			switch {
			case i < 0:
				tags = append(tags, t)
			case !proto.Equal(tags[i], t):
				r.note(f.path, 0, true, "tag %q is described differently in an earlier file; this description is left out", t.GetName())
			}
			if len(t.GetExtensions()) > 0 {
				r.note(f.path, 0, true, "the extensions of tag %q are left out", t.GetName())
			}
		}
		if v, ok := sw.GetExtensions()["x-tagGroups"]; ok {
			if groups == nil {
				groups = v.AsInterface()
			} else {
				r.note(f.path, 0, true, "x-tagGroups is left out: the base takes the groups of an earlier file; merge them by hand")
			}
		}
		for _, name := range sortedKeys(sw.GetSecurityDefinitions().GetSecurity()) {
			def := sw.GetSecurityDefinitions().GetSecurity()[name]
			if prev, ok := schemes[name]; ok {
				if !proto.Equal(prev, def) {
					r.note(f.path, 0, true, "security scheme %s differs from the one of an earlier file and is left out", name)
				}
				continue
			}
			schemes[name] = def
			schemeNames = append(schemeNames, name)
			if len(def.GetExtensions()) > 0 || len(def.GetScopes().GetScope()) > 0 {
				r.note(f.path, 0, true, "security scheme %s: its extensions and scopes are left out", name)
			}
		}
		if len(sw.GetSchemes()) > 0 || len(sw.GetConsumes()) > 0 || len(sw.GetProduces()) > 0 {
			r.note(f.path, 0, false, "schemes, consumes and produces of openapiv2_swagger are left out: OpenAPI 3 has servers, and the generator writes the content types")
		}
	}

	if info != nil {
		in := &yaml.Node{Kind: yaml.MappingNode}
		add(in, "title", str(info.GetTitle()))
		add(in, "version", str(cmpOr(info.GetVersion(), "1.0.0")))
		if d := info.GetDescription(); d != "" {
			add(in, "description", str(d))
		}
		if t := info.GetTermsOfService(); t != "" {
			add(in, "termsOfService", str(t))
		}
		if c := info.GetContact(); c != nil {
			m := &yaml.Node{Kind: yaml.MappingNode}
			for _, kv := range [][2]string{{"name", c.GetName()}, {"url", c.GetUrl()}, {"email", c.GetEmail()}} {
				if kv[1] != "" {
					add(m, kv[0], str(kv[1]))
				}
			}
			add(in, "contact", m)
		}
		if l := info.GetLicense(); l != nil {
			m := &yaml.Node{Kind: yaml.MappingNode}
			add(m, "name", str(l.GetName()))
			if l.GetUrl() != "" {
				add(m, "url", str(l.GetUrl()))
			}
			add(in, "license", m)
		}
		add(root, "info", in)
	}
	if docs != nil {
		add(root, "externalDocs", externalDocs(docs))
	}
	if len(security) > 0 {
		list := &yaml.Node{Kind: yaml.SequenceNode}
		for _, req := range security {
			m := &yaml.Node{Kind: yaml.MappingNode}
			for _, name := range sortedKeys(req.GetSecurityRequirement()) {
				scopes := &yaml.Node{Kind: yaml.SequenceNode, Style: yaml.FlowStyle}
				for _, s := range req.GetSecurityRequirement()[name].GetScope() {
					scopes.Content = append(scopes.Content, str(s))
				}
				add(m, name, scopes)
			}
			list.Content = append(list.Content, m)
		}
		add(root, "security", list)
	}
	if len(tags) > 0 {
		list := &yaml.Node{Kind: yaml.SequenceNode}
		for _, t := range tags {
			m := &yaml.Node{Kind: yaml.MappingNode}
			add(m, "name", str(t.GetName()))
			if d := t.GetDescription(); d != "" {
				add(m, "description", str(d))
			}
			if t.GetExternalDocs() != nil {
				add(m, "externalDocs", externalDocs(t.GetExternalDocs()))
			}
			list.Content = append(list.Content, m)
		}
		add(root, "tags", list)
	}
	if groups != nil {
		var n yaml.Node
		if err := n.Encode(groups); err != nil {
			return nil, err
		}
		add(root, "x-tagGroups", &n)
	}
	if len(schemeNames) > 0 {
		sec := &yaml.Node{Kind: yaml.MappingNode}
		for _, name := range schemeNames {
			s := schemes[name]
			m := &yaml.Node{Kind: yaml.MappingNode}
			switch {
			case s.GetType() == gwopts.SecurityScheme_TYPE_API_KEY && s.GetIn() == gwopts.SecurityScheme_IN_HEADER && strings.EqualFold(s.GetName(), "Authorization"):
				// Swagger 2 had no bearer scheme: an Authorization header was an API
				// key. OpenAPI 3 says what it is.
				add(m, "type", str("http"))
				add(m, "scheme", str("bearer"))
				r.note(sources[0], 0, false, "security scheme %s: an API key in the Authorization header is the bearer scheme of HTTP now", name)
			case s.GetType() == gwopts.SecurityScheme_TYPE_API_KEY:
				add(m, "type", str("apiKey"))
				add(m, "in", str(strings.ToLower(strings.TrimPrefix(s.GetIn().String(), "IN_"))))
				add(m, "name", str(s.GetName()))
			case s.GetType() == gwopts.SecurityScheme_TYPE_BASIC:
				add(m, "type", str("http"))
				add(m, "scheme", str("basic"))
			default:
				add(m, "type", str("oauth2"))
				r.note(sources[0], 0, true, "security scheme %s: OAuth2 flows are not converted; write them in the base by hand", name)
			}
			if d := s.GetDescription(); d != "" {
				add(m, "description", str(d))
			}
			add(sec, name, m)
		}
		comps := &yaml.Node{Kind: yaml.MappingNode}
		add(comps, "securitySchemes", sec)
		add(root, "components", comps)
	}

	var b bytes.Buffer
	b.WriteString("# The human part of the API description: info, security, tags and their groups.\n")
	b.WriteString("# Converted from the openapiv2_swagger options of " + strings.Join(sources, ", ") + ".\n")
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(root); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func externalDocs(d *gwopts.ExternalDocumentation) *yaml.Node {
	m := &yaml.Node{Kind: yaml.MappingNode}
	if d.GetDescription() != "" {
		m.Content = append(m.Content, str("description"), str(d.GetDescription()))
	}
	m.Content = append(m.Content, str("url"), str(d.GetUrl()))
	return m
}

func str(v string) *yaml.Node {
	n := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v}
	if strings.Contains(v, "\n") {
		n.Style = yaml.LiteralStyle
	}
	return n
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
