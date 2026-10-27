package settings

import (
	"context"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"

	"github.com/aidarbn/platform-go/kit/platform"
	"github.com/aidarbn/platform-go/kit/settingsx"
)

// Report says what an import did with every key; see settingsx.ImportReport.
type Report = settingsx.ImportReport

// Import moves settings kept elsewhere into the settings of the module, storing only the
// values that differ from the defaults of the schema. It is safe to repeat; see
// settingsx.Store.Import for the rules. The settings module must be initialised, so a
// project calls it from wireDomain or from a command built on the platform.
func Import(ctx context.Context, app *platform.App, values map[string]string, by string) (Report, error) {
	return Store(app).Import(ctx, values, by)
}

// Querier runs a query: *pgxpool.Pool, *pgx.Conn and pgx.Tx all fit.
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// ImportQuery imports the settings a query returns. The query yields two text columns,
// the key and the value, for example
//
//	SELECT key, value FROM app_configs
//
// A NULL value means "use the default", the way taply's app_configs stores it, so it is
// imported as the default of the schema: nothing is stored, and the key is reported
// among the defaults, the kept or the unknown keys.
func ImportQuery(ctx context.Context, app *platform.App, db Querier, query, by string) (Report, error) {
	values, nulls, err := readValues(ctx, db, query)
	if err != nil {
		return Report{}, err
	}

	store := Store(app)
	schema := store.Schema()
	var unknown []string
	for _, key := range nulls {
		if def, ok := schema.Definition(key); ok {
			values[key] = def.Default
		} else {
			unknown = append(unknown, key)
		}
	}

	report, err := store.Import(ctx, values, by)
	if err != nil {
		return report, err
	}
	report.Unknown = append(report.Unknown, unknown...)
	slices.Sort(report.Unknown)
	return report, nil
}

// readValues returns the values of the query and, separately, the keys whose value is
// NULL.
func readValues(ctx context.Context, db Querier, query string) (map[string]string, []string, error) {
	rows, err := db.Query(ctx, query)
	if err != nil {
		return nil, nil, fmt.Errorf("settings import: query: %w", err)
	}
	defer rows.Close()

	values := make(map[string]string)
	seen := make(map[string]bool)
	var nulls []string
	for rows.Next() {
		var key string
		var value *string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, nil, fmt.Errorf("settings import: read: %w", err)
		}
		if seen[key] {
			return nil, nil, fmt.Errorf("settings import: key %q appears twice", key)
		}
		seen[key] = true
		if value == nil {
			nulls = append(nulls, key)
			continue
		}
		values[key] = *value
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("settings import: read: %w", err)
	}
	return values, nulls, nil
}
