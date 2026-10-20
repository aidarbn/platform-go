package pgdb

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// Beginner starts a transaction: *pgxpool.Pool, *pgx.Conn and pgx.Tx all satisfy it.
type Beginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// SchemaStep is one version of the tables a platform module owns.
type SchemaStep struct {
	Version    int
	Statements []string
}

// SchemaVersionsTable records the schema version of every owner that MigrateSchema ran for.
const SchemaVersionsTable = "platform_schema_versions"

// schemaLock serialises MigrateSchema across instances and owners, so the versions table
// itself is created only once.
const schemaLock = "platform-go/schema"

// MigrateSchema brings the tables a platform module owns to its last step.
//
// Project tables live in goose migrations (Migrate); a module's own tables are versioned
// here, per owner, so a release can add a column and every database gets it. Steps run
// in one transaction under an advisory lock, so several instances starting at once apply
// each step exactly once. A database already at a newer version than the steps — a
// rollback to an older release — is left as it is: schema changes must stay readable by
// the release before them. Statements must be transactional: no CONCURRENTLY.
func MigrateSchema(ctx context.Context, db Beginner, owner string, steps []SchemaStep) error {
	if owner == "" {
		return errors.New("pgdb: schema: empty owner")
	}
	for i, s := range steps {
		if s.Version != i+1 {
			return fmt.Errorf("pgdb: schema %s: step %d has version %d, want %d", owner, i, s.Version, i+1)
		}
	}

	tx, err := db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("pgdb: schema %s: %w", owner, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1, 0))", schemaLock); err != nil {
		return fmt.Errorf("pgdb: schema %s: lock: %w", owner, err)
	}
	if _, err := tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS `+SchemaVersionsTable+` (
	owner      text        PRIMARY KEY,
	version    integer     NOT NULL,
	updated_at timestamptz NOT NULL DEFAULT now()
)`); err != nil {
		return fmt.Errorf("pgdb: schema %s: versions table: %w", owner, err)
	}

	var current int
	err = tx.QueryRow(ctx, "SELECT version FROM "+SchemaVersionsTable+" WHERE owner = $1", owner).Scan(&current)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("pgdb: schema %s: read version: %w", owner, err)
	}
	if current >= len(steps) {
		return nil
	}

	for _, s := range steps[current:] {
		for _, sql := range s.Statements {
			if _, err := tx.Exec(ctx, sql); err != nil {
				return fmt.Errorf("pgdb: schema %s: version %d: %w", owner, s.Version, err)
			}
		}
	}
	if _, err := tx.Exec(ctx, "INSERT INTO "+SchemaVersionsTable+" (owner, version) VALUES ($1, $2) "+
		"ON CONFLICT (owner) DO UPDATE SET version = EXCLUDED.version, updated_at = now()",
		owner, len(steps)); err != nil {
		return fmt.Errorf("pgdb: schema %s: record version: %w", owner, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("pgdb: schema %s: %w", owner, err)
	}
	return nil
}
