// Package rbac is a platform module: role based access to the gRPC methods of the API,
// in the model and policy format of taply by default, or in any casbin model.
//
// The module decides what a role may call; who the caller is stays with the project.
// The project's authentication interceptor puts the roles into the context with
// WithRoles, and asks Public to skip authentication for public methods.
//
// Without options beyond WithPolicy the module is taply's: the model is Model, the
// policy is the embedded rbac/policy.csv checked by ParsePolicy. WithModel, WithAdapter,
// WithFunction, WithRequest and WithPublic take it to any casbin model — role
// inheritance, domains, matchers of the project's own — and a policy in any casbin
// adapter, reloaded without a restart. Casbin gives the enforcer itself for the rest.
package rbac

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/casbin/casbin/v2"
	"github.com/casbin/casbin/v2/model"
	"github.com/casbin/casbin/v2/persist"
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

// ErrUnauthenticated is what a RequestFunc returns for a caller who has not
// authenticated: the call is answered Unauthenticated.
var ErrUnauthenticated = errors.New("rbac: authentication required")

// RequestFunc turns a call into the requests to enforce: each one is the arguments of
// casbin's Enforce, in the order of the model's request_definition, and the call is
// allowed when any of them is. The default is one request per role from the context,
// {role, method, "*"}.
//
// It returns ErrUnauthenticated for a caller who has not authenticated (Unauthenticated),
// no requests for an authenticated caller who may do nothing (PermissionDenied), and
// any other error to refuse the call: an error with a gRPC status is returned to the
// client as it is, any other one answers Internal and is logged.
type RequestFunc func(ctx context.Context, method string) ([][]any, error)

// Enforcer answers access questions. It is safe for concurrent use, a Reload included.
type Enforcer struct {
	se *casbin.SyncedEnforcer

	custom bool            // a model of the project's own
	rules  []Rule          // taply's model with a policy text: the parsed rules, with lines
	text   *textAdapter    // a policy text in a model of the project's own
	source persist.Adapter // the project's adapter; nil for a policy text

	sub, obj, act int // positions of sub, obj and act in p, -1 when the model has none

	request  RequestFunc
	public   func(method string) []any
	reloadMu sync.Mutex
	reloaded func(error) // the module counts reloads
}

// NewEnforcer builds an enforcer of taply's model from a policy text.
func NewEnforcer(policy string) (*Enforcer, error) {
	return newEnforcer(settings{policy: policy})
}

// NewEnforcerWith builds an enforcer from the options of the module, for a check
// outside the module: a command, a test, a handler of a non-gRPC route.
func NewEnforcerWith(opts ...Option) (*Enforcer, error) {
	m := New(Config{}, opts...)
	return newEnforcer(*m.settings())
}

func newEnforcer(s settings) (*Enforcer, error) {
	e := &Enforcer{custom: s.model != "", source: s.adapter, request: s.request}

	var rules []Rule
	if !e.custom && s.adapter == nil {
		parsed, err := ParsePolicy(s.policy)
		if err != nil {
			return nil, err
		}
		rules = parsed
	}
	text := Model
	if e.custom {
		text = s.model
	}
	m, err := model.NewModelFromString(text)
	if err != nil {
		return nil, fmt.Errorf("rbac: model: %w", err)
	}
	e.sub, e.obj, e.act = fieldIndex(m, "sub"), fieldIndex(m, "obj"), fieldIndex(m, "act")

	switch {
	case !e.custom && s.adapter == nil:
		// taply's model and policy, as the module always had them
		se, err := casbin.NewSyncedEnforcer(m)
		if err != nil {
			return nil, fmt.Errorf("rbac: enforcer: %w", err)
		}
		if len(rules) > 0 {
			lines := make([][]string, 0, len(rules))
			for _, r := range rules {
				lines = append(lines, []string{r.Role, r.Method, r.Action})
			}
			if _, err := se.AddPolicies(lines); err != nil {
				return nil, fmt.Errorf("rbac: policy: %w", err)
			}
		}
		e.se, e.rules = se, rules
	case s.adapter != nil:
		var a persist.Adapter = s.adapter
		if !e.custom {
			a = &checkedAdapter{Adapter: s.adapter}
		}
		if e.se, err = casbin.NewSyncedEnforcer(m, a); err != nil {
			return nil, fmt.Errorf("rbac: policy: %w", err)
		}
	default:
		e.text = &textAdapter{text: s.policy}
		if e.se, err = casbin.NewSyncedEnforcer(m, e.text); err != nil {
			return nil, fmt.Errorf("rbac: policy: %w", err)
		}
	}

	for _, f := range s.functions {
		e.se.AddFunction(f.name, f.fn)
	}

	tokens := len(m["r"]["r"].Tokens)
	switch {
	case s.public != nil:
		e.public = s.public
	case tokens == 3:
		e.public = func(method string) []any { return []any{Public, method, Public} }
	}
	if e.request == nil && tokens != 3 {
		return nil, fmt.Errorf("rbac: model: the request has %d fields, not sub, obj, act: give WithRequest", tokens)
	}
	if e.custom && e.public != nil {
		// The matcher is compiled on the first Enforce: an unknown function or field
		// stops the start instead of failing every call.
		if _, err := e.se.Enforce(e.public("/rbac.v1.Check/Model")...); err != nil {
			return nil, fmt.Errorf("rbac: model: %w", err)
		}
	}
	return e, nil
}

// fieldIndex is the position of a field in the policy definition, -1 when it has none.
func fieldIndex(m model.Model, field string) int {
	if p, ok := m["p"]["p"]; ok {
		return slices.Index(p.Tokens, "p_"+field)
	}
	return -1
}

// Public reports whether a method needs no authentication: whether the public request
// is allowed — {"*", method, "*"} unless WithPublic gives another one. A model whose
// request is not sub, obj, act has no public methods without WithPublic.
func (e *Enforcer) Public(method string) bool {
	if e.public == nil {
		return false
	}
	ok, err := e.se.Enforce(e.public(method)...)
	return err == nil && ok
}

// Allowed reports whether any of the roles may call the method: whether
// {role, method, "*"} is allowed for any of them. With WithRequest the requests are the
// project's; Check asks what the module asks.
func (e *Enforcer) Allowed(roles []string, method string) bool {
	for _, role := range roles {
		if role == Public {
			continue // a caller cannot claim the public role to reach private methods
		}
		if ok, err := e.se.Enforce(role, method, Public); err == nil && ok {
			return true
		}
	}
	return e.Public(method)
}

// Check answers whether the caller in the context may call the method, as the module's
// interceptor does: nil, or the gRPC status the caller gets.
func (e *Enforcer) Check(ctx context.Context, method string) error {
	_, err, _ := e.decide(ctx, method)
	return err
}

// decide returns the reason for the denied counter, the status for the caller and the
// cause to log, if there is one the caller does not see.
func (e *Enforcer) decide(ctx context.Context, method string) (reason string, err, cause error) {
	if e.Public(method) {
		return "", nil, nil
	}
	if e.request == nil {
		roles, ok := RolesFrom(ctx)
		if !ok {
			return "unauthenticated", status.Error(codes.Unauthenticated, "authentication required"), nil
		}
		if !e.Allowed(roles, method) {
			return "forbidden", status.Errorf(codes.PermissionDenied, "insufficient role for %s", method), nil
		}
		return "", nil, nil
	}

	requests, err := e.request(ctx, method)
	switch {
	case errors.Is(err, ErrUnauthenticated):
		return "unauthenticated", status.Error(codes.Unauthenticated, "authentication required"), nil
	case err != nil:
		if _, ok := status.FromError(err); ok {
			return "error", err, nil
		}
		return "error", status.Error(codes.Internal, "access check failed"), err
	}
	var failed error
	for _, r := range requests {
		ok, err := e.se.Enforce(r...)
		if err != nil {
			failed = err
			continue
		}
		if ok {
			return "", nil, nil
		}
	}
	if failed != nil {
		return "error", status.Error(codes.Internal, "access check failed"), failed
	}
	return "forbidden", status.Errorf(codes.PermissionDenied, "insufficient role for %s", method), nil
}

// Rules returns the policy: the rules of p, with the line of the policy text when there
// is one. For a model of the project's own a rule takes its fields from sub, obj and
// act of the policy definition, the ones it lacks stay empty; Casbin gives the rest,
// role links and domains included.
func (e *Enforcer) Rules() []Rule {
	if !e.custom && e.source == nil {
		return slices.Clone(e.rules)
	}
	policy, err := e.se.GetNamedPolicy("p")
	if err != nil {
		return nil
	}
	field := func(rule []string, i int) string {
		if i < 0 || i >= len(rule) {
			return ""
		}
		return rule[i]
	}
	rules := make([]Rule, 0, len(policy))
	for _, p := range policy {
		r := Rule{Role: field(p, e.sub), Method: field(p, e.obj), Action: field(p, e.act)}
		if e.text != nil {
			r.Line = e.text.line("p", p)
		}
		rules = append(rules, r)
	}
	return rules
}

// Unmatched returns the rules whose method pattern matches none of the given methods: a
// renamed method or a typo leaves such a rule granting nothing. For a model of the
// project's own only rules whose obj is a method pattern, starting with /, are looked at.
func (e *Enforcer) Unmatched(methods []string) []Rule {
	var out []Rule
	for _, r := range e.Rules() {
		if e.custom && !strings.HasPrefix(r.Method, "/") {
			continue
		}
		matched := slices.ContainsFunc(methods, func(m string) bool { return util.KeyMatch2(m, r.Method) })
		if !matched {
			out = append(out, r)
		}
	}
	return out
}

// Reload reads the policy from the adapter again and swaps it in whole: calls being
// checked see the old policy or the new one, never a mix. A policy that fails to load —
// or, in taply's model, breaks its format — leaves the old one in place and returns the
// error. A policy text is fixed and Reload does nothing.
func (e *Enforcer) Reload() error {
	if e.source == nil {
		return nil
	}
	e.reloadMu.Lock()
	defer e.reloadMu.Unlock()
	err := e.se.LoadPolicy()
	if err != nil {
		err = fmt.Errorf("rbac: reload: %w", err)
	}
	if e.reloaded != nil {
		e.reloaded(err)
	}
	return err
}

// Casbin returns the casbin enforcer, for what the module does not wrap: role links,
// domains, policy changes through the casbin API, a watcher. Changes to the policy it
// makes are seen by the module at once; with a policy text they live until a restart.
// In taply's model the adapter it holds checks the format on every load.
func (e *Enforcer) Casbin() *casbin.SyncedEnforcer { return e.se }

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
// embedded rbac/policy.csv. In taply's model it is checked by ParsePolicy; in a model of
// the project's own it is casbin's CSV — p, g and the other types the model defines —
// and casbin checks it, with the line of a broken rule. WithAdapter replaces it.
func WithPolicy(text string) Option { return func(m *Module) { m.settings().policy = text } }

// WithModel replaces taply's model with a casbin model of the project's own: role
// inheritance (g), domains, matchers and effects of its own. The policy must follow it.
func WithModel(text string) Option { return func(m *Module) { m.settings().model = text } }

// WithAdapter takes the policy from a casbin adapter — a database, a file, a service —
// instead of the text of WithPolicy. Reload, or WithReloadEvery, reads it again without
// a restart. In taply's model every load is checked as ParsePolicy checks a text.
func WithAdapter(a persist.Adapter) Option { return func(m *Module) { m.settings().adapter = a } }

// WithReloadEvery reloads the policy of the adapter on a period, from the start of the
// module until the service stops. A failed reload keeps the policy in force, is logged
// and counted in rbac_policy_reloads_total{result="error"}.
func WithReloadEvery(d time.Duration) Option { return func(m *Module) { m.settings().reloadEvery = d } }

// WithFunction adds a function the matcher of the model may call, as casbin's
// AddFunction does.
func WithFunction(name string, fn func(args ...any) (any, error)) Option {
	return func(m *Module) { s := m.settings(); s.functions = append(s.functions, function{name, fn}) }
}

// WithRequest replaces how a call becomes the requests to enforce; see RequestFunc. It
// is needed for a model whose request is not sub, obj, act — a domain, a tenant, an
// attribute of the caller.
func WithRequest(fn RequestFunc) Option { return func(m *Module) { m.settings().request = fn } }

// WithPublic gives the request that, when allowed, makes a method public, in the order
// of the model's request_definition. The default is {"*", method, "*"} for a request of
// sub, obj, act; a model with another request has no public methods without it.
func WithPublic(fn func(method string) []any) Option {
	return func(m *Module) { m.settings().public = fn }
}

// Configure applies options to the rbac module among the modules, for a project whose
// wiring is generated: call it in main, between platformModules and platform.Run.
func Configure(modules []platform.Module, opts ...Option) error {
	for _, m := range modules {
		if r, ok := m.(*Module); ok {
			for _, opt := range opts {
				opt(r)
			}
			return nil
		}
	}
	return errors.New("rbac: the module is not among the modules: enable rbac in platformgo.yaml")
}

type function struct {
	name string
	fn   func(args ...any) (any, error)
}

// settings are what the options set.
type settings struct {
	policy      string
	model       string
	adapter     persist.Adapter
	reloadEvery time.Duration
	functions   []function
	request     RequestFunc
	public      func(method string) []any
}

// Module implements platform.Module.
type Module struct {
	// The options sit behind a pointer so that Module stays comparable, as it was.
	opts     *settings
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

// settings returns the options, creating them for a Module built without New.
func (m *Module) settings() *settings {
	if m.opts == nil {
		m.opts = &settings{}
	}
	return m.opts
}

// Init reads the policy and puts the access check in front of every call.
func (m *Module) Init(_ context.Context, app *platform.App) error {
	enforcer, err := newEnforcer(*m.settings())
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
	if m.settings().adapter != nil {
		reloads := prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "rbac", Name: "policy_reloads_total", Help: "reloads of the access policy from its adapter",
		}, []string{"result"})
		if err := app.Metrics().Register(reloads); err != nil {
			return fmt.Errorf("rbac metrics: %w", err)
		}
		enforcer.reloaded = func(err error) {
			result := "ok"
			if err != nil {
				result = "error"
			}
			reloads.WithLabelValues(result).Inc()
		}
	}

	platform.Provide(app, enforcer)
	api.Authorize(app, m.unary, m.stream)
	api.PublicMethods(app, enforcer.Public)
	return nil
}

// Start reloads the policy on the period of WithReloadEvery, and warns about rules that
// grant nothing because no registered method matches them.
func (m *Module) Start(ctx context.Context) error {
	if s := m.settings(); s.adapter != nil && s.reloadEvery > 0 {
		go m.reloadLoop(ctx)
	}
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

// reloadLoop runs until the service stops: the context of Start is the one of the run.
func (m *Module) reloadLoop(ctx context.Context) {
	t := time.NewTicker(m.settings().reloadEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := m.enforcer.Reload(); err != nil {
				m.app.Logger().Warn("the access policy did not reload, the previous one stays in force",
					"module", "rbac", "err", err)
			}
		}
	}
}

func (m *Module) check(ctx context.Context, method string) error {
	reason, err, cause := m.enforcer.decide(ctx, method)
	if err == nil {
		return nil
	}
	if cause != nil {
		m.app.Logger().Error("access check failed", "module", "rbac", "method", method, "err", cause)
	}
	m.denied.WithLabelValues(method, reason).Inc()
	return err
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
