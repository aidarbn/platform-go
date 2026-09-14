package webx

import (
	"net/http"
	"strings"

	"golang.org/x/text/language"
)

// ContentSecurityPolicy allows nothing inline and nothing from other origins: scripts,
// styles, fonts and images come from the service itself. With it htmx must run with
// allowEval off and without inline indicator styles.
const ContentSecurityPolicy = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; " +
	"font-src 'self'; connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'"

// PageHeaders sets the headers of an HTML page: the policy, no framing, and a referrer
// policy that keeps paths with secrets in them out of other sites' logs.
func PageHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", ContentSecurityPolicy)
		h.Set("Referrer-Policy", "same-origin")
		h.Set("X-Frame-Options", "DENY")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		next.ServeHTTP(w, r)
	})
}

// NoReferrer is for pages whose address is a secret, such as a link with a token: the
// address never leaves in a Referer header and the page is never cached.
func NoReferrer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

// LocaleCookie remembers the language a visitor chose.
const LocaleCookie = "lang"

// Locale picks the language of a request: the cookie of the visitor's choice, then the
// best match of Accept-Language, then the fallback, which must be in supported.
func Locale(r *http.Request, supported []string, fallback string) string {
	if c, err := r.Cookie(LocaleCookie); err == nil {
		for _, s := range supported {
			if strings.EqualFold(c.Value, s) {
				return s
			}
		}
	}
	tags := make([]language.Tag, 0, len(supported)+1)
	tags = append(tags, language.Make(fallback))
	for _, s := range supported {
		if s != fallback {
			tags = append(tags, language.Make(s))
		}
	}
	accepted, _, err := language.ParseAcceptLanguage(r.Header.Get("Accept-Language"))
	if err != nil || len(accepted) == 0 {
		return fallback
	}
	_, index, confidence := language.NewMatcher(tags).Match(accepted...)
	if confidence == language.No {
		return fallback
	}
	if index == 0 {
		return fallback
	}
	rest := make([]string, 0, len(supported))
	for _, s := range supported {
		if s != fallback {
			rest = append(rest, s)
		}
	}
	return rest[index-1]
}
