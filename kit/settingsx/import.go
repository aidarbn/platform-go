package settingsx

import (
	"context"
	"fmt"
	"slices"
	"strings"
)

// ImportReport says what Import did with every key it was given. Each list is ordered by
// key, and a key appears in exactly one of them.
type ImportReport struct {
	Imported  []string          // stored as overrides
	Unchanged []string          // an equal override is already stored: a repeated import
	Defaults  []string          // equal to the default of the schema, so nothing is stored
	Kept      []string          // a different override is already stored and wins
	Rejected  []ImportRejection // the schema refuses the value; nothing is stored
	Unknown   []string          // the schema does not describe the key
}

// ImportRejection is a value the schema refused.
type ImportRejection struct {
	Key   string
	Value string
	Err   error
}

// Total returns how many keys the report covers.
func (r ImportReport) Total() int {
	return len(r.Imported) + len(r.Unchanged) + len(r.Defaults) + len(r.Kept) + len(r.Rejected) + len(r.Unknown)
}

// String is the report for a log or the output of a migration command.
func (r ImportReport) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "settings import: %d keys: %d imported, %d unchanged, %d defaults, %d kept, %d rejected, %d unknown",
		r.Total(), len(r.Imported), len(r.Unchanged), len(r.Defaults), len(r.Kept), len(r.Rejected), len(r.Unknown))
	for _, line := range []struct {
		name string
		keys []string
	}{
		{"imported", r.Imported},
		{"unchanged", r.Unchanged},
		{"defaults", r.Defaults},
		{"kept", r.Kept},
		{"unknown", r.Unknown},
	} {
		if len(line.keys) > 0 {
			fmt.Fprintf(&b, "\n  %s: %s", line.name, strings.Join(line.keys, ", "))
		}
	}
	for _, rej := range r.Rejected {
		fmt.Fprintf(&b, "\n  rejected: %s = %q: %v", rej.Key, rej.Value, rej.Err)
	}
	return b.String()
}

// Import moves values kept elsewhere — another table, a file, an older configuration
// service — into the store. Only overrides are stored: a value equal to the default of
// the schema, in any spelling (Equal), is skipped, so the setting keeps following the
// default across releases. A source that wrote a row for every key, defaults included,
// therefore leaves only its real overrides behind.
//
// Import never overwrites a stored override: a value an administrator set after the
// first import wins, and the key is reported as kept. That, and skipping equal values,
// make Import safe to repeat, so a project can call it on every start until the source
// is gone. Values are stored one by one; after a failure the keys already stored are
// in the report, and a repeated call carries on from there.
//
// The actor is recorded as the author of every stored value, as with Set.
func (s *Store) Import(ctx context.Context, values map[string]string, actor string) (ImportReport, error) {
	var report ImportReport

	// Another instance may have stored overrides since the last refresh, and they win.
	if err := s.Reload(ctx); err != nil {
		return report, err
	}

	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)

	for _, key := range keys {
		raw := values[key]
		def, ok := s.schema.Definition(key)
		if !ok {
			report.Unknown = append(report.Unknown, key)
			continue
		}
		if err := validateValue(def, raw); err != nil {
			report.Rejected = append(report.Rejected, ImportRejection{Key: key, Value: raw, Err: err})
			continue
		}

		s.mu.RLock()
		stored, overridden := s.overrides[key]
		s.mu.RUnlock()

		switch {
		case overridden && Equal(def, stored, raw):
			report.Unchanged = append(report.Unchanged, key)
		case overridden:
			report.Kept = append(report.Kept, key)
		case Equal(def, def.Default, raw):
			report.Defaults = append(report.Defaults, key)
		default:
			if err := s.Set(ctx, key, raw, actor); err != nil {
				return report, err
			}
			report.Imported = append(report.Imported, key)
		}
	}
	return report, nil
}
