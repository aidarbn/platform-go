package gen

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// MigrationsDir is where the SQL migrations of a project live.
const MigrationsDir = "db/migrations"

const migrationTemplate = `-- +goose Up

-- +goose Down
`

// NewMigration creates an empty goose migration named after the time and the given name
// and returns its path relative to the project. The timestamp keeps files ordered and
// avoids number clashes between branches.
func NewMigration(dir, name string, now time.Time) (string, error) {
	slug := migrationSlug(name)
	if slug == "" {
		return "", errors.New("a migration needs a name of letters or digits, for example create_orders")
	}

	rel := filepath.ToSlash(filepath.Join(MigrationsDir, now.UTC().Format("20060102150405")+"_"+slug+".sql"))
	full := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return "", err
	}

	f, err := os.OpenFile(full, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return "", fmt.Errorf("%s: %w", rel, err)
	}
	defer f.Close()
	if _, err := f.WriteString(migrationTemplate); err != nil {
		return "", fmt.Errorf("%s: %w", rel, err)
	}
	return rel, nil
}

func migrationSlug(name string) string {
	var b strings.Builder
	underscore := false
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			underscore = false
		default:
			if b.Len() > 0 && !underscore {
				b.WriteByte('_')
				underscore = true
			}
		}
	}
	return strings.Trim(b.String(), "_")
}
