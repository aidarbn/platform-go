package admin

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/aidarbn/platform-go/kit/adminx"
	"github.com/aidarbn/platform-go/kit/settingsx"
)

// reserved are the paths of the panel itself: a project page must not shadow the sign
// in or the settings.
var reserved = []string{"/login", "/logout", "/settings", "/users", "/audit"}

// server is the HTTP layer of the panel. It is separate from the module so that the
// panel can be tested over real HTTP without a database.
type server struct {
	cfg      Config
	service  string
	log      *slog.Logger
	auth     *adminx.Auth
	users    adminx.UserRepo
	audit    adminx.AuditRepo
	settings *settingsx.Store
	pages    []Page
	tmpl     map[string]*template.Template
}

// NewServer builds the panel handler. Everything it needs is passed in, so tests run it
// on in memory storage.
func NewServer(cfg Config, service string, auth *adminx.Auth, users adminx.UserRepo, aud adminx.AuditRepo, settings *settingsx.Store, pages []Page, log *slog.Logger) http.Handler {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	s := &server{
		cfg: cfg, service: service, log: log,
		auth: auth, users: users, audit: aud, settings: settings, pages: pages,
		tmpl: make(map[string]*template.Template, len(contentTemplates)),
	}
	for name, content := range contentTemplates {
		s.tmpl[name] = template.Must(template.New(name).Parse(
			pageSrc + auditTableTmpl + `{{define "content"}}` + content + `{{end}}`))
	}
	return s.routes()
}

func (s *server) routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /login", s.showLogin)
	mux.HandleFunc("POST /login", s.doLogin)
	mux.Handle("POST /logout", s.guard(nil, http.HandlerFunc(s.doLogout)))

	mux.Handle("GET /{$}", s.guard(nil, http.HandlerFunc(s.showIndex)))
	mux.Handle("GET /settings", s.guard([]string{adminx.RoleAdmin}, http.HandlerFunc(s.showSettings)))
	mux.Handle("POST /settings/set", s.guard([]string{adminx.RoleAdmin}, http.HandlerFunc(s.setSetting)))
	mux.Handle("POST /settings/reset", s.guard([]string{adminx.RoleAdmin}, http.HandlerFunc(s.resetSetting)))
	mux.Handle("GET /users", s.guard([]string{adminx.RoleAdmin}, http.HandlerFunc(s.showUsers)))
	mux.Handle("POST /users/create", s.guard([]string{adminx.RoleAdmin}, http.HandlerFunc(s.createUser)))
	mux.Handle("POST /users/toggle", s.guard([]string{adminx.RoleAdmin}, http.HandlerFunc(s.toggleUser)))
	mux.Handle("POST /users/delete", s.guard([]string{adminx.RoleAdmin}, http.HandlerFunc(s.deleteUser)))
	mux.Handle("GET /audit", s.guard([]string{adminx.RoleAdmin}, http.HandlerFunc(s.showAudit)))

	for _, p := range s.pages {
		mux.Handle(p.Path, s.guard(p.Roles, p.Handler))
	}
	return mux
}

// userKey carries the signed in user through the request context.
type userKey struct{}

// UserFrom returns the signed in user of the request: project pages use it to know who
// is acting and what they may do.
func UserFrom(ctx context.Context) (adminx.User, bool) {
	u, ok := ctx.Value(userKey{}).(adminx.User)
	return u, ok
}

// guard demands a session, checks the roles and protects the forms from cross site
// requests. Everything but the sign in goes through it.
func (s *server) guard(roles []string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := sessionToken(r)
		user, _, err := s.auth.Session(r.Context(), token)
		if err != nil {
			s.denySession(w, r, err)
			return
		}

		// Double submit: the form carries the hash of the token, which a foreign site
		// cannot read out of the cookie.
		if r.Method == http.MethodPost && r.FormValue("csrf") != adminx.SessionID(token) {
			http.Error(w, "the form has expired, open the page again", http.StatusForbidden)
			return
		}

		if !user.HasAny(roles) {
			w.WriteHeader(http.StatusForbidden)
			s.render(w, r, user, "forbidden", "Not enough rights", nil)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userKey{}, user)))
	})
}

func (s *server) denySession(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, adminx.ErrNoSession), errors.Is(err, adminx.ErrSessionExpired),
		errors.Is(err, adminx.ErrUserDisabled), errors.Is(err, adminx.ErrNoUser):
		if r.Method != http.MethodGet {
			http.Error(w, "sign in again", http.StatusUnauthorized)
			return
		}
		s.clearCookie(w)
		http.Redirect(w, r, "/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
	default:
		s.fail(w, r, err)
	}
}

func (s *server) showLogin(w http.ResponseWriter, r *http.Request) {
	// An already signed in browser has nothing to do on the sign in page.
	if _, _, err := s.auth.Session(r.Context(), sessionToken(r)); err == nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	s.renderLogin(w, r, r.URL.Query().Get("next"), "")
}

func (s *server) doLogin(w http.ResponseWriter, r *http.Request) {
	next := r.FormValue("next")
	token, err := s.auth.Login(r.Context(),
		r.FormValue("email"), r.FormValue("password"), r.FormValue("code"),
		clientIP(r), r.UserAgent())

	if err != nil {
		s.log.Info("failed sign in attempt", "email", adminx.NormalizeEmail(r.FormValue("email")), "ip", clientIP(r), "err", err)
		w.WriteHeader(http.StatusUnauthorized)
		s.renderLogin(w, r, next, loginError(err))
		return
	}

	user, _, err := s.auth.Session(r.Context(), token)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.record(r, user, adminx.ActionLogin, user.Email, "")

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		Expires:  time.Now().Add(s.sessionTTL()),
		HttpOnly: true,
		Secure:   !s.cfg.Insecure,
		SameSite: http.SameSiteStrictMode,
	})
	http.Redirect(w, r, safeNext(next), http.StatusSeeOther)
}

// loginError keeps the reason vague where it would reveal whether an account exists,
// and precise where it helps the person signing in.
func loginError(err error) string {
	switch {
	case errors.Is(err, adminx.ErrCodeRequired):
		return "The account needs a one time code."
	case errors.Is(err, adminx.ErrCodeReused):
		return "This code has already been used: wait for the next one."
	case errors.Is(err, adminx.ErrInvalidCode):
		return "Wrong one time code."
	case errors.Is(err, adminx.ErrUserDisabled):
		return "The account is disabled."
	default:
		return "Wrong email or password."
	}
}

func (s *server) doLogout(w http.ResponseWriter, r *http.Request) {
	user, _ := UserFrom(r.Context())
	if err := s.auth.Logout(r.Context(), sessionToken(r)); err != nil {
		s.fail(w, r, err)
		return
	}
	s.record(r, user, adminx.ActionLogout, user.Email, "")
	s.clearCookie(w)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (s *server) showIndex(w http.ResponseWriter, r *http.Request) {
	user, _ := UserFrom(r.Context())

	data := map[string]any{"HasSettings": s.settings != nil}
	if s.settings != nil {
		data["Settings"] = len(s.settings.Values())
		data["Overrides"] = s.settings.Overrides()
	}
	if users, err := s.users.List(r.Context()); err == nil {
		data["Users"] = len(users)
	}
	if user.Has(adminx.RoleAdmin) {
		entries, err := s.audit.List(r.Context(), 10, 0)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		data["Audit"] = entries
	}
	s.render(w, r, user, "index", "Overview", data)
}

type settingsGroupView struct {
	Name   string
	Values []settingsx.Value
}

func (s *server) showSettings(w http.ResponseWriter, r *http.Request) {
	user, _ := UserFrom(r.Context())
	if s.settings == nil {
		s.render(w, r, user, "settings", "Business settings", map[string]any{})
		return
	}

	var groups []settingsGroupView
	for _, v := range s.settings.Values() {
		if n := len(groups); n > 0 && groups[n-1].Name == v.Group {
			groups[n-1].Values = append(groups[n-1].Values, v)
			continue
		}
		groups = append(groups, settingsGroupView{Name: v.Group, Values: []settingsx.Value{v}})
	}
	s.render(w, r, user, "settings", "Business settings", map[string]any{"Groups": groups})
}

func (s *server) setSetting(w http.ResponseWriter, r *http.Request) {
	user, _ := UserFrom(r.Context())
	if s.settings == nil {
		http.Error(w, "the settings module is off", http.StatusNotFound)
		return
	}

	key, value := r.FormValue("key"), strings.TrimSpace(r.FormValue("value"))
	before := ""
	if def, ok := s.settings.Schema().Definition(key); ok {
		before = s.settings.Raw(def.Key)
	}

	if err := s.settings.Set(r.Context(), key, value, user.Email); err != nil {
		s.back(w, r, "settings", "", err.Error())
		return
	}
	s.record(r, user, adminx.ActionSettingSet, key, fmt.Sprintf("%s → %s", before, value))
	s.back(w, r, "settings", key+" saved", "")
}

func (s *server) resetSetting(w http.ResponseWriter, r *http.Request) {
	user, _ := UserFrom(r.Context())
	if s.settings == nil {
		http.Error(w, "the settings module is off", http.StatusNotFound)
		return
	}

	key := r.FormValue("key")
	if err := s.settings.Reset(r.Context(), key, user.Email); err != nil {
		s.back(w, r, "settings", "", err.Error())
		return
	}
	s.record(r, user, adminx.ActionSettingReset, key, "")
	s.back(w, r, "settings", key+" back to the default", "")
}

func (s *server) showUsers(w http.ResponseWriter, r *http.Request) {
	user, _ := UserFrom(r.Context())
	users, err := s.users.List(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, user, "users", "Accounts", map[string]any{"Users": users})
}

func (s *server) createUser(w http.ResponseWriter, r *http.Request) {
	actor, _ := UserFrom(r.Context())

	var roles []string
	for _, role := range strings.Split(r.FormValue("roles"), ",") {
		if role = strings.TrimSpace(role); role != "" {
			roles = append(roles, role)
		}
	}
	twoFactor := r.FormValue("twofactor") != ""

	created, secret, err := s.auth.CreateUser(r.Context(), r.FormValue("email"), r.FormValue("password"), roles, twoFactor)
	if err != nil {
		s.back(w, r, "users", "", err.Error())
		return
	}
	s.record(r, actor, adminx.ActionUserCreate, created.Email, strings.Join(roles, ", "))

	if !twoFactor {
		s.back(w, r, "users", created.Email+" added", "")
		return
	}
	// The secret is shown once and never again: it is not stored anywhere a person can
	// read it later.
	s.render(w, r, actor, "secret", "Two factor authentication", map[string]any{
		"Email":  created.Email,
		"Secret": secret,
		"URI":    adminx.TOTPURI(s.service, created.Email, secret),
	})
}

func (s *server) toggleUser(w http.ResponseWriter, r *http.Request) {
	actor, _ := UserFrom(r.Context())
	target, err := s.formUser(r)
	if err != nil {
		s.back(w, r, "users", "", err.Error())
		return
	}

	// Disabling your own account would lock you out of the panel.
	if target.ID == actor.ID {
		s.back(w, r, "users", "", "you cannot disable your own account")
		return
	}

	target.Disabled = !target.Disabled
	if err := s.users.Update(r.Context(), target); err != nil {
		s.back(w, r, "users", "", err.Error())
		return
	}
	state := "enabled"
	if target.Disabled {
		state = "disabled"
	}
	s.record(r, actor, adminx.ActionUserUpdate, target.Email, state)
	s.back(w, r, "users", target.Email+" "+state, "")
}

func (s *server) deleteUser(w http.ResponseWriter, r *http.Request) {
	actor, _ := UserFrom(r.Context())
	target, err := s.formUser(r)
	if err != nil {
		s.back(w, r, "users", "", err.Error())
		return
	}
	if target.ID == actor.ID {
		s.back(w, r, "users", "", "you cannot delete your own account")
		return
	}
	if err := s.users.Delete(r.Context(), target.ID); err != nil {
		s.back(w, r, "users", "", err.Error())
		return
	}
	s.record(r, actor, adminx.ActionUserDelete, target.Email, "")
	s.back(w, r, "users", target.Email+" deleted", "")
}

func (s *server) formUser(r *http.Request) (adminx.User, error) {
	id, err := strconv.ParseInt(r.FormValue("id"), 10, 64)
	if err != nil {
		return adminx.User{}, errors.New("bad account id")
	}
	return s.users.ByID(r.Context(), id)
}

func (s *server) showAudit(w http.ResponseWriter, r *http.Request) {
	user, _ := UserFrom(r.Context())

	const perPage = 50
	before, _ := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
	entries, err := s.audit.List(r.Context(), perPage, before)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	data := map[string]any{"Entries": entries}
	if len(entries) == perPage {
		data["Next"] = entries[len(entries)-1].ID
	}
	s.render(w, r, user, "audit", "Audit log", data)
}

// record writes to the audit log. A failure here must not break the action that has
// already happened, so it is logged rather than returned.
func (s *server) record(r *http.Request, user adminx.User, action, target, details string) {
	err := s.audit.Add(r.Context(), adminx.AuditEntry{
		At: time.Now(), Actor: user.Email, Action: action,
		Target: target, Details: details, IP: clientIP(r),
	})
	if err != nil {
		s.log.Error("failed to write to the audit log", "action", action, "err", err)
	}
}

type pageData struct {
	Service string
	Title   string
	User    adminx.User
	Nav     []navItem
	CSRF    string
	Message string
	Error   string
	Data    map[string]any
}

type navItem struct {
	Title  string
	Path   string
	Active bool
}

func (s *server) render(w http.ResponseWriter, r *http.Request, user adminx.User, name, title string, data map[string]any) {
	tmpl, ok := s.tmpl[name]
	if !ok {
		s.fail(w, r, fmt.Errorf("admin: no page %q", name))
		return
	}
	if data == nil {
		data = map[string]any{}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	err := tmpl.ExecuteTemplate(w, "layout", pageData{
		Service: s.service,
		Title:   title,
		User:    user,
		Nav:     s.nav(r, user),
		CSRF:    adminx.SessionID(sessionToken(r)),
		Message: r.URL.Query().Get("ok"),
		Error:   r.URL.Query().Get("error"),
		Data:    data,
	})
	if err != nil {
		s.log.Error("failed to render a page", "page", name, "err", err)
	}
}

func (s *server) renderLogin(w http.ResponseWriter, r *http.Request, next, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	err := loginTemplate.ExecuteTemplate(w, "login", loginData{
		Service: s.service,
		Error:   message,
		Next:    next,
	})
	if err != nil {
		s.log.Error("failed to render the sign in page", "err", err)
	}
}

// loginData is what the sign in page needs: there is no session and no menu yet.
type loginData struct {
	Service string
	Error   string
	Next    string
}

// nav is the menu: the pages of the panel plus the project pages this account may open.
func (s *server) nav(r *http.Request, user adminx.User) []navItem {
	items := []navItem{{Title: "Overview", Path: "/"}}
	if user.Has(adminx.RoleAdmin) {
		items = append(items,
			navItem{Title: "Settings", Path: "/settings"},
			navItem{Title: "Accounts", Path: "/users"},
			navItem{Title: "Audit", Path: "/audit"},
		)
	}
	for _, p := range s.pages {
		if user.HasAny(p.Roles) {
			items = append(items, navItem{Title: p.Title, Path: p.Path})
		}
	}
	for i := range items {
		items[i].Active = items[i].Path == r.URL.Path
	}
	return items
}

// back returns to a page with a banner. Redirecting after a form means a refresh does
// not repeat the action.
func (s *server) back(w http.ResponseWriter, r *http.Request, page, ok, failure string) {
	q := url.Values{}
	if ok != "" {
		q.Set("ok", ok)
	}
	if failure != "" {
		q.Set("error", failure)
	}
	target := "/" + page
	if len(q) > 0 {
		target += "?" + q.Encode()
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func (s *server) fail(w http.ResponseWriter, _ *http.Request, err error) {
	s.log.Error("the admin panel could not serve a request", "err", err)
	http.Error(w, "internal error", http.StatusInternalServerError)
}

func (s *server) clearCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: !s.cfg.Insecure, SameSite: http.SameSiteStrictMode,
	})
}

func (s *server) sessionTTL() time.Duration {
	if s.cfg.SessionTTL > 0 {
		return s.cfg.SessionTTL
	}
	return adminx.DefaultSessionTTL
}

func sessionToken(r *http.Request) string {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return ""
	}
	return c.Value
}

// safeNext keeps a redirect inside the panel: an open redirect would hand the sign in
// page to somebody else's site.
func safeNext(next string) string {
	if next == "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") {
		return "/"
	}
	return next
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// validatePagePath is called when a project registers a page: a page that shadows the
// sign in would lock everyone out, and that must fail loudly at startup.
func validatePagePath(path string) error {
	if !strings.HasPrefix(path, "/") {
		return fmt.Errorf("admin: page path %q must start with /", path)
	}
	if slices.Contains(reserved, strings.TrimSuffix(path, "/")) {
		return fmt.Errorf("admin: page path %q is used by the panel itself", path)
	}
	return nil
}
