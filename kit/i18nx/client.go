package i18nx

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrInvalidArgs is returned when an entity, id, field or locale is empty.
var ErrInvalidArgs = errors.New("i18n: invalid arguments")

// Table is taply's translations table.
const Table = "i18n_translations"

// DBTX is what a pool and a transaction share.
type DBTX interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	SendBatch(ctx context.Context, b *pgx.Batch) pgx.BatchResults
}

// Translations of an entity: field to locale to text.
type Translations map[string]map[Locale]string

// Client reads and writes translations.
type Client struct {
	pool          *pgxpool.Pool
	defaultLocale Locale
	locales       []Locale
	matcher       *localeMatcher

	mu        sync.RWMutex
	static    map[string]map[Locale]string
	templates []errorTemplate
}

// New creates a client. The pool may be nil for static translations only.
func New(pool *pgxpool.Pool, defaultLocale Locale, locales ...Locale) *Client {
	if defaultLocale == "" {
		defaultLocale = LocaleRU
	}
	if len(locales) > 0 && !slices.Contains(locales, defaultLocale) {
		locales = append([]Locale{defaultLocale}, locales...)
	}
	c := &Client{pool: pool, defaultLocale: defaultLocale, locales: locales, static: map[string]map[Locale]string{}}
	if len(locales) > 0 {
		c.matcher = newLocaleMatcher(locales, defaultLocale)
	}
	return c
}

// EnsureSchema creates taply's translations table on the first run.
func EnsureSchema(ctx context.Context, db DBTX) error {
	for _, sql := range []string{
		`CREATE TABLE IF NOT EXISTS ` + Table + ` (
	id         bigserial   PRIMARY KEY,
	entity     text        NOT NULL,
	entity_id  text        NOT NULL,
	field      text        NOT NULL,
	locale     text        NOT NULL,
	text       text        NOT NULL,
	created_at timestamptz NOT NULL DEFAULT now(),
	updated_at timestamptz NOT NULL DEFAULT now(),
	UNIQUE (entity, entity_id, field, locale)
)`,
		`CREATE INDEX IF NOT EXISTS idx_i18n_translations_lookup ON ` + Table + ` (entity, entity_id, locale)`,
	} {
		if _, err := db.Exec(ctx, sql); err != nil {
			return fmt.Errorf("i18n: create table: %w", err)
		}
	}
	return nil
}

// DefaultLocale is the locale of the base columns and the fallback of every lookup.
func (c *Client) DefaultLocale() Locale { return c.defaultLocale }

// Locales are the supported locales, the default first.
func (c *Client) Locales() []Locale { return slices.Clone(c.locales) }

// ResolveLocale returns the locale of a request: the one set with WithLocale, or the
// best match of Accept-Language among the supported locales, or the default.
func (c *Client) ResolveLocale(ctx context.Context) Locale {
	header := acceptLanguage(ctx)
	if l, ok := ctx.Value(localeKey{}).(Locale); ok && l != "" {
		header = string(l)
	}
	if header == "" {
		return c.defaultLocale
	}
	if c.matcher != nil {
		return c.matcher.match(header)
	}
	return newLocaleMatcher([]Locale{c.defaultLocale}, c.defaultLocale).match(header)
}

// WithTx returns a client that works inside a transaction.
func (c *Client) WithTx(tx pgx.Tx) *TxClient { return &TxClient{db: tx} }

// Set saves one translation.
func (c *Client) Set(ctx context.Context, entity, entityID, field string, locale Locale, text string) error {
	return set(ctx, c.pool, entity, entityID, field, locale, text)
}

// SetBatch saves the translations of an entity.
func (c *Client) SetBatch(ctx context.Context, entity, entityID string, t Translations) error {
	return setBatch(ctx, c.pool, entity, entityID, t, false)
}

// SetBatchIfMissing saves only the translations that do not exist yet, so a sync does
// not overwrite what an editor changed.
func (c *Client) SetBatchIfMissing(ctx context.Context, entity, entityID string, t Translations) error {
	return setBatch(ctx, c.pool, entity, entityID, t, true)
}

// GetAll returns the translations of an entity.
func (c *Client) GetAll(ctx context.Context, entity, entityID string) (Translations, error) {
	return getAll(ctx, c.pool, entity, entityID)
}

// GetBatchAll returns the translations of several entities: id to translations.
func (c *Client) GetBatchAll(ctx context.Context, entity string, entityIDs []string) (map[string]Translations, error) {
	return getBatchAll(ctx, c.pool, entity, entityIDs)
}

// TxClient writes translations in a transaction.
type TxClient struct{ db DBTX }

// Set saves one translation.
func (t *TxClient) Set(ctx context.Context, entity, entityID, field string, locale Locale, text string) error {
	return set(ctx, t.db, entity, entityID, field, locale, text)
}

// SetBatch saves the translations of an entity.
func (t *TxClient) SetBatch(ctx context.Context, entity, entityID string, tr Translations) error {
	return setBatch(ctx, t.db, entity, entityID, tr, false)
}

// SetBatchIfMissing saves only the missing translations.
func (t *TxClient) SetBatchIfMissing(ctx context.Context, entity, entityID string, tr Translations) error {
	return setBatch(ctx, t.db, entity, entityID, tr, true)
}

// GetAll returns the translations of an entity.
func (t *TxClient) GetAll(ctx context.Context, entity, entityID string) (Translations, error) {
	return getAll(ctx, t.db, entity, entityID)
}

func set(ctx context.Context, db DBTX, entity, entityID, field string, locale Locale, text string) error {
	// An empty entity id is a shared translation, such as the title of a menu.
	if entity == "" || field == "" || locale == "" {
		return fmt.Errorf("%w: entity, field and locale must not be empty", ErrInvalidArgs)
	}
	_, err := db.Exec(ctx, `INSERT INTO `+Table+` (entity, entity_id, field, locale, text) VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (entity, entity_id, field, locale) DO UPDATE SET text = excluded.text, updated_at = now()`,
		entity, entityID, field, string(locale), text)
	if err != nil {
		return fmt.Errorf("i18n: set: %w", err)
	}
	return nil
}

func setBatch(ctx context.Context, db DBTX, entity, entityID string, t Translations, onlyMissing bool) error {
	if len(t) == 0 {
		return nil
	}
	if entity == "" {
		return fmt.Errorf("%w: entity must not be empty", ErrInvalidArgs)
	}
	conflict := "DO UPDATE SET text = excluded.text, updated_at = now()"
	if onlyMissing {
		conflict = "DO NOTHING"
	}
	query := `INSERT INTO ` + Table + ` (entity, entity_id, field, locale, text) VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (entity, entity_id, field, locale) ` + conflict

	batch := &pgx.Batch{}
	for field, locales := range t {
		for locale, text := range locales {
			batch.Queue(query, entity, entityID, field, string(locale), text)
		}
	}
	results := db.SendBatch(ctx, batch)
	defer func() { _ = results.Close() }()
	for range batch.Len() {
		if _, err := results.Exec(); err != nil {
			return fmt.Errorf("i18n: set batch: %w", err)
		}
	}
	return nil
}

func getAll(ctx context.Context, db DBTX, entity, entityID string) (Translations, error) {
	if entity == "" {
		return nil, fmt.Errorf("%w: entity must not be empty", ErrInvalidArgs)
	}
	rows, err := db.Query(ctx, `SELECT field, locale, text FROM `+Table+` WHERE entity = $1 AND entity_id = $2`, entity, entityID)
	if err != nil {
		return nil, fmt.Errorf("i18n: get: %w", err)
	}
	defer rows.Close()
	out := Translations{}
	for rows.Next() {
		var field, locale, text string
		if err := rows.Scan(&field, &locale, &text); err != nil {
			return nil, err
		}
		if out[field] == nil {
			out[field] = map[Locale]string{}
		}
		out[field][Locale(locale)] = text
	}
	return out, rows.Err()
}

func getBatchAll(ctx context.Context, db DBTX, entity string, ids []string) (map[string]Translations, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := db.Query(ctx, `SELECT entity_id, field, locale, text FROM `+Table+` WHERE entity = $1 AND entity_id = ANY($2)`, entity, ids)
	if err != nil {
		return nil, fmt.Errorf("i18n: get batch: %w", err)
	}
	defer rows.Close()
	out := map[string]Translations{}
	for rows.Next() {
		var id, field, locale, text string
		if err := rows.Scan(&id, &field, &locale, &text); err != nil {
			return nil, err
		}
		if out[id] == nil {
			out[id] = Translations{}
		}
		if out[id][field] == nil {
			out[id][field] = map[Locale]string{}
		}
		out[id][field][Locale(locale)] = text
	}
	return out, rows.Err()
}
