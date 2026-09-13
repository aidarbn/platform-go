package admin_test

import (
	"context"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aidarbn/platform-go/kit/adminx"
	"github.com/aidarbn/platform-go/kit/modules/admin"
	"github.com/aidarbn/platform-go/kit/settingsx"
)

// Hashing is deliberately slow, so the accounts of the tests share one hash.
var testHash = sync.OnceValue(func() string {
	hash, err := adminx.HashPassword("hunter2")
	if err != nil {
		panic(err)
	}
	return hash
})

type panel struct {
	t        *testing.T
	srv      *httptest.Server
	client   *http.Client
	store    *adminx.MemoryStore
	settings *settingsx.Store
}

func newPanel(t *testing.T, pages ...admin.Page) *panel {
	t.Helper()

	store := adminx.NewMemoryStore()
	ctx := context.Background()
	for _, u := range []adminx.User{
		{Email: "admin@example.com", PasswordHash: testHash(), Roles: []string{adminx.RoleAdmin}},
		{Email: "support@example.com", PasswordHash: testHash(), Roles: []string{"support"}},
	} {
		if _, err := store.Create(ctx, u); err != nil {
			t.Fatalf("Create: %v", err)
		}
	}

	schema := settingsx.MustSchema(
		settingsx.Definition{Key: "api.ratelimit.rps", Group: "api.ratelimit", Name: "rps", Kind: settingsx.KindInt, Default: "50", Min: "1", Max: "1000",
			Description: "Requests per second per client", GroupDescription: "Limits of the public API", RequiresRestart: true},
		settingsx.Definition{Key: "app.maintenance", Group: "app", Name: "maintenance", Kind: settingsx.KindBool, Default: "false"},
		settingsx.Definition{Key: "app.mode", Group: "app", Name: "mode", Kind: settingsx.KindString, Default: "fast", Options: []string{"fast", "slow"}},
	)
	settings := settingsx.NewTestStore(schema, nil)

	handler := admin.NewServer(
		admin.Config{Insecure: true, SessionTTL: time.Hour},
		"shop-api",
		adminx.NewAuth(store.Users(), store.Sessions(), time.Hour),
		store.Users(), store.Audit(), settings, pages, nil,
	)

	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &panel{t: t, srv: srv, client: &http.Client{Jar: jar}, store: store, settings: settings}
}

func (p *panel) get(path string) (int, string) {
	p.t.Helper()
	resp, err := p.client.Get(p.srv.URL + path)
	if err != nil {
		p.t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

func (p *panel) post(path string, form url.Values) (*http.Response, string) {
	p.t.Helper()
	resp, err := p.client.PostForm(p.srv.URL+path, form)
	if err != nil {
		p.t.Fatalf("POST %s: %v", path, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp, string(body)
}

// login signs in and returns the CSRF token the forms carry.
func (p *panel) login(email, password string) string {
	p.t.Helper()

	resp, body := p.post("/login", url.Values{"email": {email}, "password": {password}})
	if resp.StatusCode != http.StatusOK {
		p.t.Fatalf("login: %d\n%s", resp.StatusCode, body)
	}
	return p.csrf()
}

func (p *panel) csrf() string {
	p.t.Helper()

	base, err := url.Parse(p.srv.URL)
	if err != nil {
		p.t.Fatal(err)
	}
	for _, c := range p.client.Jar.Cookies(base) {
		if c.Name == "platform_admin_session" {
			return adminx.SessionID(c.Value)
		}
	}
	p.t.Fatal("no session cookie")
	return ""
}

func (p *panel) auditActions() []string {
	p.t.Helper()

	entries, err := p.store.Audit().List(context.Background(), 50, 0)
	if err != nil {
		p.t.Fatalf("audit: %v", err)
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Action)
	}
	return out
}

func TestSignInRequired(t *testing.T) {
	p := newPanel(t)

	// A page opened without a session leads to the sign in form.
	code, body := p.get("/settings")
	if code != http.StatusOK || !strings.Contains(body, "Sign in") {
		t.Fatalf("code = %d\n%s", code, body)
	}
}

func TestLoginAndLogout(t *testing.T) {
	p := newPanel(t)

	resp, body := p.post("/login", url.Values{"email": {"admin@example.com"}, "password": {"wrong"}})
	if resp.StatusCode != http.StatusUnauthorized || !strings.Contains(body, "Wrong email or password") {
		t.Fatalf("a wrong password: %d\n%s", resp.StatusCode, body)
	}

	csrf := p.login("admin@example.com", "hunter2")
	if code, body := p.get("/"); code != http.StatusOK || !strings.Contains(body, "Overview") {
		t.Fatalf("the overview: %d\n%s", code, body)
	}

	resp, _ = p.post("/logout", url.Values{"csrf": {csrf}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("logout: %d", resp.StatusCode)
	}
	if code, body := p.get("/"); !strings.Contains(body, "Sign in") {
		t.Fatalf("after the logout: %d\n%s", code, body)
	}
	if got := p.auditActions(); len(got) != 2 || got[0] != adminx.ActionLogout || got[1] != adminx.ActionLogin {
		t.Errorf("the log = %v", got)
	}
}

// A one time code that is asked for must actually be checked.
func TestLoginWithTwoFactor(t *testing.T) {
	p := newPanel(t)

	ctx := context.Background()
	user, err := p.store.ByEmail(ctx, "admin@example.com")
	if err != nil {
		t.Fatal(err)
	}
	secret, err := adminx.NewTOTPSecret()
	if err != nil {
		t.Fatal(err)
	}
	user.TOTPSecret = secret
	if err := p.store.Update(ctx, user); err != nil {
		t.Fatal(err)
	}

	resp, body := p.post("/login", url.Values{"email": {"admin@example.com"}, "password": {"hunter2"}})
	if resp.StatusCode != http.StatusUnauthorized || !strings.Contains(body, "needs a one time code") {
		t.Fatalf("without a code: %d\n%s", resp.StatusCode, body)
	}

	code, err := adminx.TOTPCode(secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	resp, body = p.post("/login", url.Values{
		"email": {"admin@example.com"}, "password": {"hunter2"}, "code": {code},
	})
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "Overview") {
		t.Fatalf("with a code: %d\n%s", resp.StatusCode, body)
	}
}

// The panel must not become a redirect to somebody else's site.
func TestLoginIgnoresForeignNext(t *testing.T) {
	p := newPanel(t)

	resp, body := p.post("/login", url.Values{
		"email": {"admin@example.com"}, "password": {"hunter2"}, "next": {"//evil.example.com/"},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login: %d", resp.StatusCode)
	}
	if !strings.Contains(body, "Overview") {
		t.Errorf("the redirect left the panel:\n%s", body)
	}
	if host := resp.Request.URL.Host; host != strings.TrimPrefix(p.srv.URL, "http://") {
		t.Errorf("ended up at %s", resp.Request.URL)
	}
}

func TestSettingsPage(t *testing.T) {
	p := newPanel(t)
	csrf := p.login("admin@example.com", "hunter2")

	code, body := p.get("/settings")
	if code != http.StatusOK {
		t.Fatalf("code = %d", code)
	}
	for _, want := range []string{"api.ratelimit", "app.maintenance", "app.mode", "Requests per second per client", "Limits of the public API", "applies after a restart"} {
		if !strings.Contains(body, want) {
			t.Errorf("the page lacks %q", want)
		}
	}

	// Saving a value changes what the application reads and lands in the log.
	resp, body := p.post("/settings/set", url.Values{"csrf": {csrf}, "key": {"api.ratelimit.rps"}, "value": {"120"}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("set: %d\n%s", resp.StatusCode, body)
	}
	if got := p.settings.Int("api.ratelimit.rps"); got != 120 {
		t.Errorf("rps = %d", got)
	}
	if !strings.Contains(body, "saved") {
		t.Errorf("no confirmation:\n%s", body)
	}

	// A value the schema refuses must not be stored, and the reason must be shown.
	resp, body = p.post("/settings/set", url.Values{"csrf": {csrf}, "key": {"api.ratelimit.rps"}, "value": {"0"}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("set: %d", resp.StatusCode)
	}
	if got := p.settings.Int("api.ratelimit.rps"); got != 120 {
		t.Errorf("a bad value was stored: %d", got)
	}
	if !strings.Contains(body, "below the minimum") {
		t.Errorf("no reason shown:\n%s", body)
	}

	// Reset brings the default back.
	resp, body = p.post("/settings/reset", url.Values{"csrf": {csrf}, "key": {"api.ratelimit.rps"}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("reset: %d\n%s", resp.StatusCode, body)
	}
	if got := p.settings.Int("api.ratelimit.rps"); got != 50 {
		t.Errorf("after the reset rps = %d", got)
	}

	if got := p.auditActions(); len(got) != 3 ||
		got[0] != adminx.ActionSettingReset || got[1] != adminx.ActionSettingSet {
		t.Errorf("the log = %v", got)
	}
}

// Without the token a form submitted from another site must not work.
func TestFormsNeedCSRF(t *testing.T) {
	p := newPanel(t)
	p.login("admin@example.com", "hunter2")

	for name, form := range map[string]url.Values{
		"no token":    {"key": {"api.ratelimit.rps"}, "value": {"200"}},
		"wrong token": {"csrf": {"nonsense"}, "key": {"api.ratelimit.rps"}, "value": {"200"}},
	} {
		t.Run(name, func(t *testing.T) {
			resp, _ := p.post("/settings/set", form)
			if resp.StatusCode != http.StatusForbidden {
				t.Fatalf("code = %d", resp.StatusCode)
			}
			if got := p.settings.Int("api.ratelimit.rps"); got != 50 {
				t.Errorf("the value changed: %d", got)
			}
		})
	}
}

// The settings and the accounts are for admins; support must not see them.
func TestRolesGuardPages(t *testing.T) {
	p := newPanel(t)
	p.login("support@example.com", "hunter2")

	for _, path := range []string{"/settings", "/users", "/audit"} {
		code, body := p.get(path)
		if code != http.StatusForbidden || !strings.Contains(body, "Not enough rights") {
			t.Errorf("%s: %d\n%s", path, code, body)
		}
	}
	if code, body := p.get("/"); code != http.StatusOK || !strings.Contains(body, "Overview") {
		t.Errorf("the overview: %d\n%s", code, body)
	}
}

func TestUsersPage(t *testing.T) {
	p := newPanel(t)
	csrf := p.login("admin@example.com", "hunter2")

	code, body := p.get("/users")
	if code != http.StatusOK || !strings.Contains(body, "support@example.com") {
		t.Fatalf("code = %d\n%s", code, body)
	}

	// The secret of a new account is shown once, right after it is created.
	resp, body := p.post("/users/create", url.Values{
		"csrf": {csrf}, "email": {"ops@example.com"}, "password": {"letmein1"},
		"roles": {"support, billing"}, "twofactor": {"1"},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create: %d\n%s", resp.StatusCode, body)
	}
	if !strings.Contains(body, "otpauth://totp/shop-api:ops@example.com") {
		t.Errorf("no link for the authenticator app:\n%s", body)
	}

	created, err := p.store.ByEmail(context.Background(), "ops@example.com")
	if err != nil {
		t.Fatalf("the account was not created: %v", err)
	}
	if !created.TwoFactor() || !created.Has("billing") {
		t.Errorf("account = %+v", created)
	}

	// Disabling and deleting somebody else's account works.
	resp, body = p.post("/users/toggle", url.Values{"csrf": {csrf}, "id": {itoa(created.ID)}})
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "disabled") {
		t.Fatalf("toggle: %d\n%s", resp.StatusCode, body)
	}
	resp, body = p.post("/users/delete", url.Values{"csrf": {csrf}, "id": {itoa(created.ID)}})
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, "deleted") {
		t.Fatalf("delete: %d\n%s", resp.StatusCode, body)
	}
	if _, err := p.store.ByEmail(context.Background(), "ops@example.com"); err == nil {
		t.Error("the account survived")
	}
}

// Locking yourself out of the panel must not be one click away.
func TestCannotDisableOrDeleteYourself(t *testing.T) {
	p := newPanel(t)
	csrf := p.login("admin@example.com", "hunter2")

	me, err := p.store.ByEmail(context.Background(), "admin@example.com")
	if err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{"/users/toggle", "/users/delete"} {
		resp, body := p.post(path, url.Values{"csrf": {csrf}, "id": {itoa(me.ID)}})
		if resp.StatusCode != http.StatusOK || !strings.Contains(body, "your own account") {
			t.Errorf("%s: %d\n%s", path, resp.StatusCode, body)
		}
	}
	if _, err := p.store.ByEmail(context.Background(), "admin@example.com"); err != nil {
		t.Errorf("the account is gone: %v", err)
	}
}

// A project page gets the layout, the roles and the signed in user.
func TestProjectPage(t *testing.T) {
	var seen string
	page := admin.Page{
		Title: "Orders",
		Path:  "/orders",
		Roles: []string{"support"},
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			user, ok := admin.UserFrom(r.Context())
			if !ok {
				http.Error(w, "no user", http.StatusInternalServerError)
				return
			}
			seen = user.Email
			_, _ = w.Write([]byte("the orders of " + user.Email))
		}),
	}

	p := newPanel(t, page)
	p.login("support@example.com", "hunter2")

	code, body := p.get("/orders")
	if code != http.StatusOK || !strings.Contains(body, "the orders of support@example.com") {
		t.Fatalf("code = %d\n%s", code, body)
	}
	if seen != "support@example.com" {
		t.Errorf("the page saw %q", seen)
	}
	// The menu shows the page to those who may open it.
	if _, body := p.get("/"); !strings.Contains(body, `href="/orders"`) {
		t.Errorf("the page is missing from the menu:\n%s", body)
	}
}

func TestProjectPageRespectsRoles(t *testing.T) {
	page := admin.Page{Title: "Billing", Path: "/billing", Roles: []string{"billing"},
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })}

	p := newPanel(t, page)
	p.login("support@example.com", "hunter2")

	if code, _ := p.get("/billing"); code != http.StatusForbidden {
		t.Errorf("code = %d", code)
	}
	if _, body := p.get("/"); strings.Contains(body, `href="/billing"`) {
		t.Errorf("a page without rights is in the menu:\n%s", body)
	}
}

// An admin may open every page, so a new role does not need a migration.
func TestAdminSeesProjectPages(t *testing.T) {
	page := admin.Page{Title: "Billing", Path: "/billing", Roles: []string{"billing"},
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })}

	p := newPanel(t, page)
	p.login("admin@example.com", "hunter2")

	if code, body := p.get("/billing"); code != http.StatusOK || !strings.Contains(body, "ok") {
		t.Errorf("code = %d\n%s", code, body)
	}
}

func TestAuditPage(t *testing.T) {
	p := newPanel(t)
	csrf := p.login("admin@example.com", "hunter2")

	if _, body := p.post("/settings/set", url.Values{
		"csrf": {csrf}, "key": {"app.maintenance"}, "value": {"true"},
	}); !strings.Contains(body, "saved") {
		t.Fatalf("set:\n%s", body)
	}

	code, body := p.get("/audit")
	if code != http.StatusOK {
		t.Fatalf("code = %d", code)
	}
	for _, want := range []string{adminx.ActionSettingSet, "app.maintenance", "false → true", "admin@example.com"} {
		if !strings.Contains(body, want) {
			t.Errorf("the log page lacks %q:\n%s", want, body)
		}
	}
}

// A session that has expired stops working, and the cookie stops being offered.
func TestExpiredSessionSendsBackToLogin(t *testing.T) {
	p := newPanel(t)
	p.login("admin@example.com", "hunter2")

	ctx := context.Background()
	if _, err := p.store.Sessions().DeleteExpired(ctx, time.Now().Add(2*time.Hour)); err != nil {
		t.Fatalf("DeleteExpired: %v", err)
	}
	if code, body := p.get("/"); code != http.StatusOK || !strings.Contains(body, "Sign in") {
		t.Fatalf("code = %d\n%s", code, body)
	}
}

// A page whose path would shadow the sign in must fail loudly at startup.
func TestRegistryRejectsBadPaths(t *testing.T) {
	for name, path := range map[string]string{
		"no slash": "orders",
		"reserved": "/login",
		"settings": "/settings",
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("want a panic")
				}
			}()
			var r admin.Registry
			r.Add(admin.Page{Title: "x", Path: path})
		})
	}
}

func itoa(id int64) string { return strconv.FormatInt(id, 10) }
