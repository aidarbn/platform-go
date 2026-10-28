package rbac_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/casbin/casbin/v2/model"
	"github.com/casbin/casbin/v2/persist"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/aidarbn/platform-go/kit/modules/api"
	"github.com/aidarbn/platform-go/kit/modules/rbac"
	"github.com/aidarbn/platform-go/kit/platform"
)

// memAdapter is a policy store the test changes between reloads.
type memAdapter struct {
	mu    sync.Mutex
	lines []string
	fail  error
	loads int
}

func (a *memAdapter) set(lines ...string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.lines = lines
}

func (a *memAdapter) LoadPolicy(m model.Model) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.loads++
	if a.fail != nil {
		return a.fail
	}
	for _, l := range a.lines {
		if err := persist.LoadPolicyLine(l, m); err != nil {
			return err
		}
	}
	return nil
}

func (a *memAdapter) SavePolicy(model.Model) error                              { return nil }
func (a *memAdapter) AddPolicy(string, string, []string) error                  { return nil }
func (a *memAdapter) RemovePolicy(string, string, []string) error               { return nil }
func (a *memAdapter) RemoveFilteredPolicy(string, string, int, ...string) error { return nil }

// Role inheritance: alice is an editor, an editor inherits viewer.
const inheritModel = `[request_definition]
r = sub, obj, act

[policy_definition]
p = sub, obj, act

[role_definition]
g = _, _

[policy_effect]
e = some(where (p.eft == allow))

[matchers]
m = (g(r.sub, p.sub) || r.sub == p.sub) && keyMatch2(r.obj, p.obj) && (p.act == "*" || r.act == p.act)
`

const inheritPolicy = `# casbin's CSV, g included
p, *, /grpc.health.v1.Health/*, *
p, viewer, /shop.v1.Orders/Get, *
p, editor, /shop.v1.Orders/*, *
p, editor, orders-report, read

g, editor, viewer
g, alice, editor
`

func TestModelWithRoleInheritance(t *testing.T) {
	e, err := rbac.NewEnforcerWith(rbac.WithModel(inheritModel), rbac.WithPolicy(inheritPolicy))
	if err != nil {
		t.Fatal(err)
	}
	if !e.Public("/grpc.health.v1.Health/Check") || e.Public("/shop.v1.Orders/Get") {
		t.Error("public methods are wrong")
	}
	if !e.Allowed([]string{"alice"}, "/shop.v1.Orders/Delete") {
		t.Error("alice inherits editor")
	}
	if !e.Allowed([]string{"viewer"}, "/shop.v1.Orders/Get") || e.Allowed([]string{"viewer"}, "/shop.v1.Orders/Delete") {
		t.Error("viewer may only get")
	}
	ctx := rbac.WithRoles(context.Background(), "alice")
	if err := e.Check(ctx, "/shop.v1.Orders/Delete"); err != nil {
		t.Errorf("Check alice: %v", err)
	}
	if err := e.Check(context.Background(), "/shop.v1.Orders/Get"); status.Code(err) != codes.Unauthenticated {
		t.Errorf("Check without roles: %v", err)
	}
	if err := e.Check(rbac.WithRoles(context.Background(), "*"), "/shop.v1.Orders/Get"); status.Code(err) != codes.PermissionDenied {
		t.Errorf("the public role claim: %v", err)
	}

	rules := e.Rules()
	if len(rules) != 4 || rules[1] != (rbac.Rule{Role: "viewer", Method: "/shop.v1.Orders/Get", Action: "*", Line: 3}) {
		t.Errorf("rules = %+v", rules)
	}
	// orders-report is no method pattern: it is not reported.
	un := e.Unmatched([]string{"/shop.v1.Orders/Get", "/grpc.health.v1.Health/Check"})
	if len(un) != 0 {
		t.Errorf("unmatched = %+v", un)
	}
	un = e.Unmatched([]string{"/grpc.health.v1.Health/Check"})
	if len(un) != 2 || un[0].Line != 3 || un[1].Line != 4 {
		t.Errorf("unmatched = %+v", un)
	}

	// The casbin enforcer is at hand for what the module does not wrap.
	if roles, _ := e.Casbin().GetImplicitRolesForUser("alice"); len(roles) != 2 {
		t.Errorf("implicit roles of alice = %v", roles)
	}
	if _, err := e.Casbin().AddGroupingPolicy("bob", "viewer"); err != nil {
		t.Fatal(err)
	}
	if !e.Allowed([]string{"bob"}, "/shop.v1.Orders/Get") {
		t.Error("a role link added through casbin is not seen")
	}
	if e.Reload() != nil {
		t.Error("Reload of a policy text must do nothing")
	}
}

// Domains: a user has a role in a tenant; the tenant comes from the call.
const domainModel = `[request_definition]
r = sub, dom, obj, act

[policy_definition]
p = sub, dom, obj, act

[role_definition]
g = _, _, _

[policy_effect]
e = some(where (p.eft == allow))

[matchers]
m = (g(r.sub, p.sub, r.dom) || p.sub == "*") && (p.dom == "*" || r.dom == p.dom) && keyMatch2(r.obj, p.obj) && (p.act == "*" || r.act == p.act)
`

const domainPolicy = `p, *, *, /grpc.health.v1.Health/*, *
p, admin, *, /shop.v1.Orders/*, *
p, clerk, *, /shop.v1.Orders/Get, *
g, alice, admin, shop-1
g, alice, clerk, shop-2
`

type tenantKey struct{}

func tenantRequest(ctx context.Context, method string) ([][]any, error) {
	user, ok := rbac.RolesFrom(ctx)
	if !ok {
		return nil, rbac.ErrUnauthenticated
	}
	tenant, _ := ctx.Value(tenantKey{}).(string)
	switch tenant {
	case "":
		return nil, status.Error(codes.InvalidArgument, "no tenant")
	case "broken":
		return nil, errors.New("tenant store down")
	}
	var reqs [][]any
	for _, u := range user {
		reqs = append(reqs, []any{u, tenant, method, "*"})
	}
	return reqs, nil
}

func TestModelWithDomains(t *testing.T) {
	opts := []rbac.Option{
		rbac.WithModel(domainModel), rbac.WithPolicy(domainPolicy), rbac.WithRequest(tenantRequest),
	}
	if _, err := rbac.NewEnforcerWith(opts...); err != nil {
		t.Fatal(err)
	}
	e, err := rbac.NewEnforcerWith(append(opts, rbac.WithPublic(func(m string) []any { return []any{"anyone", "any", m, "*"} }))...)
	if err != nil {
		t.Fatal(err)
	}
	if !e.Public("/grpc.health.v1.Health/Check") || e.Public("/shop.v1.Orders/Get") {
		t.Error("public methods are wrong")
	}
	in := func(tenant string) context.Context {
		return context.WithValue(rbac.WithRoles(context.Background(), "alice"), tenantKey{}, tenant)
	}
	for _, tc := range []struct {
		ctx    context.Context
		method string
		want   codes.Code
	}{
		{in("shop-1"), "/shop.v1.Orders/Delete", codes.OK},
		{in("shop-2"), "/shop.v1.Orders/Get", codes.OK},
		{in("shop-2"), "/shop.v1.Orders/Delete", codes.PermissionDenied},
		{in("shop-3"), "/shop.v1.Orders/Get", codes.PermissionDenied},
		{in(""), "/shop.v1.Orders/Get", codes.InvalidArgument},
		{in("broken"), "/shop.v1.Orders/Get", codes.Internal},
		{context.Background(), "/shop.v1.Orders/Get", codes.Unauthenticated},
		{context.Background(), "/grpc.health.v1.Health/Check", codes.OK},
	} {
		if got := status.Code(e.Check(tc.ctx, tc.method)); got != tc.want {
			t.Errorf("%v %s: %v, want %v", tc.ctx.Value(tenantKey{}), tc.method, got, tc.want)
		}
	}
	// Without WithPublic a request of four fields has no public method.
	e2, _ := rbac.NewEnforcerWith(opts...)
	if e2.Public("/grpc.health.v1.Health/Check") {
		t.Error("a public method without WithPublic")
	}
	rules := e.Rules()
	if len(rules) != 3 || rules[0].Role != "*" || rules[0].Method != "/grpc.health.v1.Health/*" || rules[0].Line != 1 {
		t.Errorf("rules = %+v", rules)
	}
}

func TestModelErrors(t *testing.T) {
	for name, tc := range map[string]struct {
		opts []rbac.Option
		want string
	}{
		"broken model": {[]rbac.Option{rbac.WithModel("[request_definition]\nr = sub\n")}, "rbac: model:"},
		"request of four fields, no WithRequest": {
			[]rbac.Option{rbac.WithModel(domainModel), rbac.WithPolicy(domainPolicy)}, "give WithRequest",
		},
		"rule of the wrong size, with its line": {
			[]rbac.Option{rbac.WithModel(inheritModel), rbac.WithPolicy("p, a, /x/*, *\n\np, a, /x/*\n")}, "policy line 3: invalid policy rule size",
		},
		"unknown type": {
			[]rbac.Option{rbac.WithModel(Model2()), rbac.WithPolicy("g, a, b\n")}, "policy line 1",
		},
		"unknown function in the matcher": {
			[]rbac.Option{rbac.WithModel(strings.Replace(inheritModel, "keyMatch2(", "inShop(", 1))}, "rbac: model:",
		},
		"taply's format still checked by default": {
			[]rbac.Option{rbac.WithPolicy("g, a, b\n")}, "policy line 1: expected p, role, method, action",
		},
	} {
		_, err := rbac.NewEnforcerWith(tc.opts...)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", name, err, tc.want)
		}
	}
}

// Model2 is taply's model given as one of the project's own: casbin checks the policy.
func Model2() string { return rbac.Model }

func TestFunction(t *testing.T) {
	m := strings.Replace(inheritModel, "keyMatch2(r.obj, p.obj)", "inService(r.obj, p.obj)", 1)
	calls := 0
	var mu sync.Mutex
	inService := func(args ...any) (any, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		return strings.HasPrefix(args[0].(string), strings.TrimSuffix(args[1].(string), "*")), nil
	}
	e, err := rbac.NewEnforcerWith(rbac.WithModel(m), rbac.WithPolicy(inheritPolicy), rbac.WithFunction("inService", inService))
	if err != nil {
		t.Fatal(err)
	}
	if !e.Allowed([]string{"editor"}, "/shop.v1.Orders/Delete") || e.Allowed([]string{"viewer"}, "/shop.v1.Orders/Delete") {
		t.Error("the matcher with a function of the project decides wrong")
	}
	if calls == 0 {
		t.Error("the function was not called")
	}
}

func TestAdapterInTaplysModel(t *testing.T) {
	a := &memAdapter{}
	a.set("p, *, /grpc.health.v1.Health/*, *", "p, support, /shop.v1.Orders/Get, *")
	e, err := rbac.NewEnforcerWith(rbac.WithAdapter(a), rbac.WithPolicy("ignored: the adapter replaces the text"))
	if err != nil {
		t.Fatal(err)
	}
	if !e.Allowed([]string{"support"}, "/shop.v1.Orders/Get") || e.Allowed([]string{"support"}, "/shop.v1.Orders/Delete") {
		t.Fatal("the policy of the adapter is wrong")
	}
	if rules := e.Rules(); len(rules) != 2 || rules[1] != (rbac.Rule{Role: "support", Method: "/shop.v1.Orders/Get", Action: "*"}) {
		t.Errorf("rules = %+v", rules)
	}

	a.set("p, *, /grpc.health.v1.Health/*, *", "p, support, /shop.v1.Orders/*, *")
	if err := e.Reload(); err != nil {
		t.Fatal(err)
	}
	if !e.Allowed([]string{"support"}, "/shop.v1.Orders/Delete") {
		t.Error("Reload did not take the new policy")
	}

	// A rule out of taply's format fails the reload and the policy in force stays.
	a.set("p, support, orders, *")
	if err := e.Reload(); err == nil || !strings.Contains(err.Error(), `method "orders" must start with /`) {
		t.Errorf("Reload of a broken policy: %v", err)
	}
	if !e.Allowed([]string{"support"}, "/shop.v1.Orders/Delete") || !e.Public("/grpc.health.v1.Health/Check") {
		t.Error("a broken reload changed the policy")
	}
	a.fail = errors.New("database down")
	if err := e.Reload(); err == nil || !strings.Contains(err.Error(), "database down") {
		t.Errorf("Reload of a failing adapter: %v", err)
	}
	if !e.Allowed([]string{"support"}, "/shop.v1.Orders/Delete") {
		t.Error("a failed reload changed the policy")
	}

	// And a broken policy stops the start.
	b := &memAdapter{}
	b.set("p, support, orders, *")
	if _, err := rbac.NewEnforcerWith(rbac.WithAdapter(b)); err == nil || !strings.Contains(err.Error(), "rbac: policy:") {
		t.Errorf("a broken policy of the adapter: %v", err)
	}
}

func TestAdapterInAModelOfItsOwn(t *testing.T) {
	a := &memAdapter{}
	a.set("p, editor, /shop.v1.Orders/*, *", "g, alice, editor")
	e, err := rbac.NewEnforcerWith(rbac.WithModel(inheritModel), rbac.WithAdapter(a))
	if err != nil {
		t.Fatal(err)
	}
	if !e.Allowed([]string{"alice"}, "/shop.v1.Orders/Get") {
		t.Fatal("alice is an editor")
	}
	a.set("p, editor, /shop.v1.Orders/*, *")
	if err := e.Reload(); err != nil {
		t.Fatal(err)
	}
	if e.Allowed([]string{"alice"}, "/shop.v1.Orders/Get") {
		t.Error("the role link removed from the adapter still grants")
	}
	a.set("p, editor, /shop.v1.Orders/*")
	if err := e.Reload(); err == nil {
		t.Error("a rule of the wrong size was loaded")
	}
	if !e.Allowed([]string{"editor"}, "/shop.v1.Orders/Get") {
		t.Error("a broken reload changed the policy")
	}
}

// Calls are checked while the policy is reloaded: run with -race.
func TestReloadUnderLoad(t *testing.T) {
	a := &memAdapter{}
	a.set("p, editor, /shop.v1.Orders/*, *", "g, alice, editor")
	e, err := rbac.NewEnforcerWith(rbac.WithModel(inheritModel), rbac.WithAdapter(a))
	if err != nil {
		t.Fatal(err)
	}
	ctx := rbac.WithRoles(context.Background(), "alice")
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				// Both policies grant alice Get: a mix of the two would not.
				if err := e.Check(ctx, "/shop.v1.Orders/Get"); err != nil {
					t.Errorf("Check during reload: %v", err)
					return
				}
				_ = e.Rules()
				_ = e.Unmatched([]string{"/shop.v1.Orders/Get"})
			}
		}()
	}
	for i := range 200 {
		if i%2 == 0 {
			a.set("p, viewer, /shop.v1.Orders/Get, *", "g, alice, viewer")
		} else {
			a.set("p, editor, /shop.v1.Orders/*, *", "g, alice, editor")
		}
		if err := e.Reload(); err != nil {
			t.Fatal(err)
		}
	}
	close(stop)
	wg.Wait()
}

// Configure finds the module among the others, and says so when it is not there.
func TestConfigureWithoutTheModule(t *testing.T) {
	if err := rbac.Configure([]platform.Module{api.New(api.Config{})}, rbac.WithModel(inheritModel)); err == nil {
		t.Error("Configure without the module")
	}
}

func ExampleWithRequest() {
	// The tenant comes from the call; the user's roles in it decide.
	e, err := rbac.NewEnforcerWith(
		rbac.WithModel(domainModel),
		rbac.WithPolicy(domainPolicy),
		rbac.WithRequest(func(ctx context.Context, method string) ([][]any, error) {
			users, ok := rbac.RolesFrom(ctx)
			if !ok {
				return nil, rbac.ErrUnauthenticated
			}
			tenant, _ := ctx.Value(tenantKey{}).(string)
			var reqs [][]any
			for _, u := range users {
				reqs = append(reqs, []any{u, tenant, method, "*"})
			}
			return reqs, nil
		}),
		rbac.WithPublic(func(method string) []any { return []any{"", "", method, "*"} }),
	)
	if err != nil {
		panic(err)
	}
	ctx := context.WithValue(rbac.WithRoles(context.Background(), "alice"), tenantKey{}, "shop-2")
	fmt.Println(status.Code(e.Check(ctx, "/shop.v1.Orders/Delete")))
	// Output: PermissionDenied
}
