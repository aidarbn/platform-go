package webx

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"io"
	"net/http"

	"github.com/a-h/templ"
)

// CSRFField is the form field holding the token; CSRFHeader carries it for htmx requests.
const (
	CSRFField  = "csrf_token"
	CSRFHeader = "X-CSRF-Token"
)

type csrfKey struct{}

// CSRF protects forms twice: the browser's own Sec-Fetch-Site and Origin headers refuse
// cross site requests (http.CrossOriginProtection), and a token in every form must match
// the cookie, for browsers that send neither header.
type CSRF struct {
	cookie  string
	secure  bool
	protect *http.CrossOriginProtection
}

// NewCSRF builds the protection. secure false allows the cookie over plain http, for
// local development only.
func NewCSRF(secure bool) *CSRF {
	name := "__Host-csrf"
	if !secure {
		name = "csrf"
	}
	return &CSRF{cookie: name, secure: secure, protect: http.NewCrossOriginProtection()}
}

// Middleware issues the token and checks it on every unsafe request.
func (c *CSRF) Middleware(next http.Handler) http.Handler {
	checked := c.protect.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := ""
		if cookie, err := r.Cookie(c.cookie); err == nil && len(cookie.Value) == 43 {
			token = cookie.Value
		}
		if token == "" {
			raw := make([]byte, 32)
			_, _ = rand.Read(raw)
			token = base64.RawURLEncoding.EncodeToString(raw)
			http.SetCookie(w, &http.Cookie{Name: c.cookie, Value: token, Path: "/", HttpOnly: true, Secure: c.secure, SameSite: http.SameSiteLaxMode})
		}
		if !safeMethod(r.Method) {
			sent := r.Header.Get(CSRFHeader)
			if sent == "" {
				sent = r.PostFormValue(CSRFField)
			}
			if subtle.ConstantTimeCompare([]byte(sent), []byte(token)) != 1 {
				http.Error(w, "the form has expired, reload the page", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), csrfKey{}, token)))
	}))
	return checked
}

func safeMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
}

// CSRFToken returns the token of the request for a form or an hx-headers attribute.
func CSRFToken(ctx context.Context) string {
	token, _ := ctx.Value(csrfKey{}).(string)
	return token
}

// CSRFInput is the hidden form field with the token.
func CSRFInput() templ.Component {
	return templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
		_, err := io.WriteString(w, `<input type="hidden" name="`+CSRFField+`" value="`+templ.EscapeString(CSRFToken(ctx))+`">`)
		return err
	})
}
