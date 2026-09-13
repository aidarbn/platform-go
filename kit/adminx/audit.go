package adminx

import (
	"context"
	"time"
)

// AuditEntry is one recorded action. The log is append only: it answers who changed
// what, and an entry that can be edited answers nothing.
type AuditEntry struct {
	ID      int64
	At      time.Time
	Actor   string // the email of the signed in user
	Action  string // what was done, "settings.set"
	Target  string // what it was done to, the setting key
	Details string // what changed, in a form a human reads
	IP      string
}

// AuditRepo stores the log.
type AuditRepo interface {
	Add(ctx context.Context, e AuditEntry) error

	// List returns the entries newest first. A zero before means from the beginning;
	// otherwise the entries older than that id are returned.
	List(ctx context.Context, limit int, before int64) ([]AuditEntry, error)
}

// Actions recorded by the admin panel itself. Project pages use their own names.
const (
	ActionLogin        = "admin.login"
	ActionLogout       = "admin.logout"
	ActionSettingSet   = "settings.set"
	ActionSettingReset = "settings.reset"
	ActionUserCreate   = "admin.user.create"
	ActionUserUpdate   = "admin.user.update"
	ActionUserDelete   = "admin.user.delete"
)
