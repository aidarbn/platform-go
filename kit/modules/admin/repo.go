package admin

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aidarbn/platform-go/kit/adminx"
)

// Table names of the admin panel. They are platform state rather than project data, so
// the module owns them instead of the project migrations.
const (
	usersTable    = "platform_admin_users"
	sessionsTable = "platform_admin_sessions"
	auditTable    = "platform_admin_audit"
)

// EnsureSchema creates the tables of the admin panel on the first run.
func EnsureSchema(ctx context.Context, pool *pgxpool.Pool) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS ` + usersTable + ` (
	id            bigserial   PRIMARY KEY,
	email         text        NOT NULL UNIQUE,
	password_hash text        NOT NULL,
	totp_secret   text        NOT NULL DEFAULT '',
	totp_counter  bigint      NOT NULL DEFAULT 0,
	roles         text[]      NOT NULL DEFAULT '{}',
	disabled      boolean     NOT NULL DEFAULT false,
	created_at    timestamptz NOT NULL DEFAULT now(),
	last_login_at timestamptz
)`,
		`CREATE TABLE IF NOT EXISTS ` + sessionsTable + ` (
	id         text        PRIMARY KEY,
	user_id    bigint      NOT NULL REFERENCES ` + usersTable + `(id) ON DELETE CASCADE,
	created_at timestamptz NOT NULL,
	expires_at timestamptz NOT NULL,
	ip         text        NOT NULL DEFAULT '',
	user_agent text        NOT NULL DEFAULT ''
)`,
		`CREATE INDEX IF NOT EXISTS ` + sessionsTable + `_user_id_idx ON ` + sessionsTable + ` (user_id)`,
		`CREATE INDEX IF NOT EXISTS ` + sessionsTable + `_expires_at_idx ON ` + sessionsTable + ` (expires_at)`,
		`CREATE TABLE IF NOT EXISTS ` + auditTable + ` (
	id      bigserial   PRIMARY KEY,
	at      timestamptz NOT NULL DEFAULT now(),
	actor   text        NOT NULL,
	action  text        NOT NULL,
	target  text        NOT NULL DEFAULT '',
	details text        NOT NULL DEFAULT '',
	ip      text        NOT NULL DEFAULT ''
)`,
	}

	for _, sql := range statements {
		if _, err := pool.Exec(ctx, sql); err != nil {
			return fmt.Errorf("admin: create schema: %w", err)
		}
	}
	return nil
}

// NewUserRepo returns the accounts stored in PostgreSQL.
func NewUserRepo(pool *pgxpool.Pool) adminx.UserRepo { return &userRepo{pool: pool} }

// NewSessionRepo returns the sessions stored in PostgreSQL.
func NewSessionRepo(pool *pgxpool.Pool) adminx.SessionRepo { return &sessionRepo{pool: pool} }

// NewAuditRepo returns the audit log stored in PostgreSQL.
func NewAuditRepo(pool *pgxpool.Pool) adminx.AuditRepo { return &auditRepo{pool: pool} }

type userRepo struct{ pool *pgxpool.Pool }

const userColumns = "id, email, password_hash, totp_secret, totp_counter, roles, disabled, created_at, last_login_at"

func scanUser(row pgx.Row) (adminx.User, error) {
	var (
		u    adminx.User
		last *time.Time
	)
	err := row.Scan(&u.ID, &u.Email, &u.PasswordHash, &u.TOTPSecret, &u.TOTPCounter,
		&u.Roles, &u.Disabled, &u.CreatedAt, &last)
	if errors.Is(err, pgx.ErrNoRows) {
		return adminx.User{}, adminx.ErrNoUser
	}
	if err != nil {
		return adminx.User{}, err
	}
	if last != nil {
		u.LastLoginAt = *last
	}
	return u, nil
}

func (r *userRepo) ByEmail(ctx context.Context, email string) (adminx.User, error) {
	return scanUser(r.pool.QueryRow(ctx,
		"SELECT "+userColumns+" FROM "+usersTable+" WHERE email = $1", adminx.NormalizeEmail(email)))
}

func (r *userRepo) ByID(ctx context.Context, id int64) (adminx.User, error) {
	return scanUser(r.pool.QueryRow(ctx,
		"SELECT "+userColumns+" FROM "+usersTable+" WHERE id = $1", id))
}

func (r *userRepo) Create(ctx context.Context, u adminx.User) (adminx.User, error) {
	roles := u.Roles
	if roles == nil {
		roles = []string{}
	}
	created := u.CreatedAt
	if created.IsZero() {
		created = time.Now()
	}

	row := r.pool.QueryRow(ctx,
		"INSERT INTO "+usersTable+" (email, password_hash, totp_secret, totp_counter, roles, disabled, created_at) "+
			"VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING "+userColumns,
		adminx.NormalizeEmail(u.Email), u.PasswordHash, u.TOTPSecret, u.TOTPCounter, roles, u.Disabled, created)
	return scanUser(row)
}

func (r *userRepo) Update(ctx context.Context, u adminx.User) error {
	roles := u.Roles
	if roles == nil {
		roles = []string{}
	}
	var last *time.Time
	if !u.LastLoginAt.IsZero() {
		last = &u.LastLoginAt
	}

	tag, err := r.pool.Exec(ctx,
		"UPDATE "+usersTable+" SET email = $2, password_hash = $3, totp_secret = $4, totp_counter = $5, "+
			"roles = $6, disabled = $7, last_login_at = $8 WHERE id = $1",
		u.ID, adminx.NormalizeEmail(u.Email), u.PasswordHash, u.TOTPSecret, u.TOTPCounter, roles, u.Disabled, last)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return adminx.ErrNoUser
	}
	return nil
}

func (r *userRepo) List(ctx context.Context) ([]adminx.User, error) {
	rows, err := r.pool.Query(ctx, "SELECT "+userColumns+" FROM "+usersTable+" ORDER BY email")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []adminx.User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (r *userRepo) Delete(ctx context.Context, id int64) error {
	tag, err := r.pool.Exec(ctx, "DELETE FROM "+usersTable+" WHERE id = $1", id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return adminx.ErrNoUser
	}
	return nil
}

type sessionRepo struct{ pool *pgxpool.Pool }

func (r *sessionRepo) Create(ctx context.Context, s adminx.Session) error {
	_, err := r.pool.Exec(ctx,
		"INSERT INTO "+sessionsTable+" (id, user_id, created_at, expires_at, ip, user_agent) "+
			"VALUES ($1, $2, $3, $4, $5, $6)",
		s.ID, s.UserID, s.CreatedAt, s.ExpiresAt, s.IP, s.UserAgent)
	return err
}

func (r *sessionRepo) ByID(ctx context.Context, id string) (adminx.Session, error) {
	var s adminx.Session
	err := r.pool.QueryRow(ctx,
		"SELECT id, user_id, created_at, expires_at, ip, user_agent FROM "+sessionsTable+" WHERE id = $1", id).
		Scan(&s.ID, &s.UserID, &s.CreatedAt, &s.ExpiresAt, &s.IP, &s.UserAgent)
	if errors.Is(err, pgx.ErrNoRows) {
		return adminx.Session{}, adminx.ErrNoSession
	}
	if err != nil {
		return adminx.Session{}, err
	}
	return s, nil
}

func (r *sessionRepo) Delete(ctx context.Context, id string) error {
	tag, err := r.pool.Exec(ctx, "DELETE FROM "+sessionsTable+" WHERE id = $1", id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return adminx.ErrNoSession
	}
	return nil
}

func (r *sessionRepo) DeleteByUser(ctx context.Context, userID int64) error {
	_, err := r.pool.Exec(ctx, "DELETE FROM "+sessionsTable+" WHERE user_id = $1", userID)
	return err
}

func (r *sessionRepo) DeleteExpired(ctx context.Context, now time.Time) (int, error) {
	tag, err := r.pool.Exec(ctx, "DELETE FROM "+sessionsTable+" WHERE expires_at <= $1", now)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

type auditRepo struct{ pool *pgxpool.Pool }

func (r *auditRepo) Add(ctx context.Context, e adminx.AuditEntry) error {
	at := e.At
	if at.IsZero() {
		at = time.Now()
	}
	_, err := r.pool.Exec(ctx,
		"INSERT INTO "+auditTable+" (at, actor, action, target, details, ip) VALUES ($1, $2, $3, $4, $5, $6)",
		at, e.Actor, e.Action, e.Target, e.Details, e.IP)
	return err
}

func (r *auditRepo) List(ctx context.Context, limit int, before int64) ([]adminx.AuditEntry, error) {
	if limit <= 0 {
		limit = 50
	}

	sql := "SELECT id, at, actor, action, target, details, ip FROM " + auditTable
	args := []any{limit}
	if before > 0 {
		sql += " WHERE id < $2"
		args = append(args, before)
	}
	sql += " ORDER BY id DESC LIMIT $1"

	rows, err := r.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []adminx.AuditEntry
	for rows.Next() {
		var e adminx.AuditEntry
		if err := rows.Scan(&e.ID, &e.At, &e.Actor, &e.Action, &e.Target, &e.Details, &e.IP); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
