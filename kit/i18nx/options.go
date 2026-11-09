package i18nx

import "github.com/jackc/pgx/v5/pgxpool"

// Option configures a Client built with NewWith, as taply's i18n.New is configured.
type Option func(*options)

type options struct {
	defaultLocale Locale
	locales       []Locale
}

// WithDefaultLocale sets the locale of the base columns and the fallback of every lookup.
func WithDefaultLocale(locale Locale) Option {
	return func(o *options) { o.defaultLocale = locale }
}

// WithLocales sets the supported locales: Accept-Language is matched against them, with
// the default locale as the fallback.
func WithLocales(locales ...Locale) Option {
	return func(o *options) { o.locales = locales }
}

// NewWith creates a client from options, the form taply's i18n.New has; New is the same
// with the locales given as arguments. Without options the default locale is Default().
func NewWith(pool *pgxpool.Pool, opts ...Option) *Client {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	return New(pool, o.defaultLocale, o.locales...)
}
