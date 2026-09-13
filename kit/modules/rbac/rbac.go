// Package rbac is a platform module: role based access to the gRPC methods of the API,
// in the model and policy format of taply.
//
// The module decides what a role may call; who the caller is stays with the project.
// The project's authentication interceptor puts the roles into the context with
// WithRoles, and asks Public to skip authentication for public methods.
package rbac

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/casbin/casbin/v2"
	"github.com/casbin/casbin/v2/model"
	"github.com/casbin/casbin/v2/util"
	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/aidarbn/platform-go/kit/confx"
	"github.com/aidarbn/platform-go/kit/modules/api"
	"github.com/aidarbn/platform-go/kit/platform"
)

// Model is taply's casbin model: a role may call a method matching a keyMatch2 pattern,
// such as /shop.v1.OrdersService/* for a whole service.
const Model = `[request_definition]
r = sub, obj, act

[policy_definition]
p = sub, obj, act

[policy_effect]
e = some(where (p.eft == allow))

[matchers]
m = r.sub == p.sub && keyMatch2(r.obj, p.obj) && (p.act == "*" || r.act == p.act)
`

// Public is the role that makes a method public: no authentication is needed.
const Public = "*"

// Config holds module settings. The module has no environment variables: access rules
// are code, reviewed and deployed with the service.
type Config struct{}

// Load reads the module settings from environment variables.
func Load(*confx.Loader) Config { return Config{} }

// Rule is one line of the policy.
type Rule struct {
	Role   string
	Method string // keyMatch2 pattern
	Action string
	Line   int
}

// ParsePolicy reads a policy in taply's CSV format: "p, role, method, action" per line,
// # comments and empty lines allowed. Every broken line is reported.
func ParsePolicy(text string) ([]Rule, error) {
	var (
		rules []Rule
		errs  []error
	)
	scanner := bufio.NewScanner(strings.NewReader(text))
	for n := 1; scanner.Scan(); n++ {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, ",")
		for i := range fields {
			fields[i] = strings.TrimSpace(fields[i])
		}
		switch {
		case len(fields) != 4 || fields[0] != "p":
			errs = append(errs, fmt.Errorf("policy line %d: expected p, role, method, action: %q", n, line))
		case fields[1] == "" || fields[2] == "" || fields[3] == "":
			errs = append(errs, fmt.Errorf("policy line %d: role, method and action must not be empty", n))
		case !strings.HasPrefix(fields[2], "/"):
			errs = append(errs, fmt.Errorf("policy line %d: method %q must start with /, as in /shop.v1.OrdersService/*", n, fields[2]))
		default:
			rules = append(rules, Rule{Role: fields[1], Method: fields[2], Action: fields[3], Line: n})
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return rules, nil
}

// Enforcer answers access questions.
type Enforcer struct {
	e     *casbin.Enforcer
	rules []Rule
}

// NewEnforcer builds an enforcer from a policy text.
func NewEnforcer(policy string) (*Enforcer, error) {
	rules, err := ParsePolicy(policy)
	if err != nil {
		return nil, err
	}
	m, err := model.NewModelFromString(Model)
	if err != nil {
		return nil, fmt.Errorf("rbac: model: %w", err)
	}
	e, err := casbin.NewEnforcer(m)
	if err != nil {
		return nil, fmt.Errorf("rbac: enforcer: %w", err)
	}
	if len(rules) > 0 {
		lines := make([][]string, 0, len(rules))
		for _, r := range rules {
			lines = append(lines, []string{r.Role, r.Method, r.Action})
		}
		if _, err := e.AddPolicies(lines); err != nil {
			return nil, fmt.Errorf("rbac: policy: %w", err)
		}
	}
	return &Enforcer{e: e, rules: rules}, nil
}

// Public reports whether a method needs no authentication.
func (e *Enforcer) Public(method string) bool {
	ok, err := e.e.Enforce(Public, method, Public)
	return err == nil && ok
}

// Allowed reports whether any of the roles may call the method.
func (e *Enforcer) Allowed(roles []string, method string) bool {
	for _, role := range roles {
		if role == Public {
			continue // a caller cannot claim the public role to reach private methods
		}
		if ok, err := e.e.Enforce(role, method, Public); err == nil && ok {
			return true
		}
	}
	return e.Public(method)
}

// Rules returns the policy.
func (e *Enforcer) Rules() []Rule { return slices.Clone(e.rules) }

// Unmatched returns the rules whose method pattern matches none of the given methods: a
// renamed method or a typo leaves such a rule granting nothing.
func (e *Enforcer) Unmatched(methods []string) []Rule {
	var out []Rule
	for _, r := range e.rules {
		matched := slices.ContainsFunc(methods, func(m string) bool { return util.KeyMatch2(m, r.Method) })
		if !matched {
			out = append(out, r)
		}
	}
	return out
}

type rolesKey struct{}

// WithRoles returns a context carrying the roles of the authenticated caller. The
// project's authentication interceptor calls it.
func WithRoles(ctx context.Context, roles ...string) context.Context {
	return context.WithValue(ctx, rolesKey{}, slices.Clone(roles))
}

// RolesFrom returns the roles of the caller and whether the caller is authenticated.
func RolesFrom(ctx context.Context) ([]string, bool) {
	roles, ok := ctx.Value(rolesKey{}).([]string)
	return roles, ok
}

// IsPublic reports whether a method needs no authentication, for the project's
// authentication interceptor.
func IsPublic(app *platform.App, method string) bool { return From(app).Public(method) }

// From returns the enforcer from the container.
func From(app *platform.App) *Enforcer { return platform.Get[*Enforcer](app) }

// Option configures the module.
type Option func(*Module)

// WithPolicy gives the module the policy of the project. The generated wiring passes the
// embedded rbac/policy.csv.
func WithPolicy(text string) Option { return func(m *Module) { m.policy = text } }

// Module implements platform.Module.
type Module struct {
	policy   string
	enforcer *Enforcer
	app      *platform.App
	denied   *prometheus.CounterVec
}

// New creates the module.
func New(_ Config, opts ...Option) *Module {
	m := &Module{}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

func (m *Module) Name() string { return "rbac" }

// Init reads the policy and puts the access check in front of every call.
func (m *Module) Init(_ context.Context, app *platform.App) error {
	enforcer, err := NewEnforcer(m.policy)
	if err != nil {
		return err
	}
	m.enforcer, m.app = enforcer, app

	m.denied = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "rbac", Name: "denied_total", Help: "calls refused by the access policy",
	}, []string{"method", "reason"})
	if err := app.Metrics().Register(m.denied); err != nil {
		return fmt.Errorf("rbac metrics: %w", err)
	}

	platform.Provide(app, enforcer)
	api.Authorize(app, m.unary, m.stream)
	return nil
}

// Start warns about rules that grant nothing because no registered method matches them.
func (m *Module) Start(context.Context) error {
	if !m.app.Serves(platform.RoleAPI) {
		return nil
	}
	methods := api.Methods(m.app)
	for _, r := range m.enforcer.Unmatched(methods) {
		m.app.Logger().Warn("an access rule matches no gRPC method", "module", "rbac",
			"line", r.Line, "role", r.Role, "method", r.Method)
	}
	return nil
}

func (m *Module) check(ctx context.Context, method string) error {
	if m.enforcer.Public(method) {
		return nil
	}
	roles, ok := RolesFrom(ctx)
	if !ok {
		m.denied.WithLabelValues(method, "unauthenticated").Inc()
		return status.Error(codes.Unauthenticated, "authentication required")
	}
	if !m.enforcer.Allowed(roles, method) {
		m.denied.WithLabelValues(method, "forbidden").Inc()
		return status.Errorf(codes.PermissionDenied, "insufficient role for %s", method)
	}
	return nil
}

func (m *Module) unary(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	if err := m.check(ctx, info.FullMethod); err != nil {
		return nil, err
	}
	return handler(ctx, req)
}

func (m *Module) stream(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	if err := m.check(ss.Context(), info.FullMethod); err != nil {
		return err
	}
	return handler(srv, ss)
}
