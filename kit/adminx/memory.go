package adminx

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"time"
)

// MemoryStore keeps accounts, sessions and the log in memory. It is what tests of the
// admin panel and of project pages run on, with no database involved.
type MemoryStore struct {
	mu       sync.Mutex
	users    []User
	sessions map[string]Session
	audit    []AuditEntry
	nextID   int64
}

// NewMemoryStore creates an empty store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{sessions: make(map[string]Session), nextID: 1}
}

// ByEmail returns an account by address.
func (s *MemoryStore) ByEmail(_ context.Context, email string) (User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, u := range s.users {
		if u.Email == NormalizeEmail(email) {
			return u, nil
		}
	}
	return User{}, ErrNoUser
}

// ByID returns an account by id.
func (s *MemoryStore) ByID(_ context.Context, id int64) (User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, u := range s.users {
		if u.ID == id {
			return u, nil
		}
	}
	return User{}, ErrNoUser
}

// Create adds an account.
func (s *MemoryStore) Create(_ context.Context, u User) (User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	u.Email = NormalizeEmail(u.Email)
	for _, other := range s.users {
		if other.Email == u.Email {
			return User{}, fmt.Errorf("adminx: %s already exists", u.Email)
		}
	}
	u.ID = s.nextID
	s.nextID++
	s.users = append(s.users, u)
	return u, nil
}

// Update replaces an account.
func (s *MemoryStore) Update(_ context.Context, u User) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i, other := range s.users {
		if other.ID == u.ID {
			s.users[i] = u
			return nil
		}
	}
	return ErrNoUser
}

// List returns the accounts ordered by address.
func (s *MemoryStore) List(context.Context) ([]User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := slices.Clone(s.users)
	slices.SortFunc(out, func(a, b User) int {
		switch {
		case a.Email < b.Email:
			return -1
		case a.Email > b.Email:
			return 1
		default:
			return 0
		}
	})
	return out, nil
}

// Delete removes an account together with its sessions.
func (s *MemoryStore) Delete(_ context.Context, id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	before := len(s.users)
	s.users = slices.DeleteFunc(s.users, func(u User) bool { return u.ID == id })
	if len(s.users) == before {
		return ErrNoUser
	}
	for key, session := range s.sessions {
		if session.UserID == id {
			delete(s.sessions, key)
		}
	}
	return nil
}

func (s *MemoryStore) createSession(_ context.Context, session Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[session.ID] = session
	return nil
}

func (s *MemoryStore) sessionByID(_ context.Context, id string) (Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	session, ok := s.sessions[id]
	if !ok {
		return Session{}, ErrNoSession
	}
	return session, nil
}

func (s *MemoryStore) deleteSession(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.sessions[id]; !ok {
		return ErrNoSession
	}
	delete(s.sessions, id)
	return nil
}

func (s *MemoryStore) deleteSessionsByUser(_ context.Context, userID int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for key, session := range s.sessions {
		if session.UserID == userID {
			delete(s.sessions, key)
		}
	}
	return nil
}

func (s *MemoryStore) deleteExpiredSessions(_ context.Context, now time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var n int
	for key, session := range s.sessions {
		if !session.ExpiresAt.After(now) {
			delete(s.sessions, key)
			n++
		}
	}
	return n, nil
}

func (s *MemoryStore) addAudit(_ context.Context, e AuditEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	e.ID = int64(len(s.audit) + 1)
	if e.At.IsZero() {
		e.At = time.Now()
	}
	s.audit = append(s.audit, e)
	return nil
}

func (s *MemoryStore) listAudit(_ context.Context, limit int, before int64) ([]AuditEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var out []AuditEntry
	for i := len(s.audit) - 1; i >= 0; i-- {
		e := s.audit[i]
		if before > 0 && e.ID >= before {
			continue
		}
		out = append(out, e)
		if limit > 0 && len(out) == limit {
			break
		}
	}
	return out, nil
}

// Users returns the store as a UserRepo.
func (s *MemoryStore) Users() UserRepo { return s }

// Sessions returns the store as a SessionRepo.
func (s *MemoryStore) Sessions() SessionRepo { return sessionView{s} }

// Audit returns the store as an AuditRepo.
func (s *MemoryStore) Audit() AuditRepo { return auditView{s} }

// sessionView and auditView expose the store as the matching repository: the store
// keeps accounts, sessions and the log at once, and UserRepo already claims the plain
// method names.
type sessionView struct{ s *MemoryStore }

func (v sessionView) Create(ctx context.Context, session Session) error {
	return v.s.createSession(ctx, session)
}
func (v sessionView) ByID(ctx context.Context, id string) (Session, error) {
	return v.s.sessionByID(ctx, id)
}
func (v sessionView) Delete(ctx context.Context, id string) error {
	return v.s.deleteSession(ctx, id)
}
func (v sessionView) DeleteByUser(ctx context.Context, userID int64) error {
	return v.s.deleteSessionsByUser(ctx, userID)
}
func (v sessionView) DeleteExpired(ctx context.Context, now time.Time) (int, error) {
	return v.s.deleteExpiredSessions(ctx, now)
}

type auditView struct{ s *MemoryStore }

func (v auditView) Add(ctx context.Context, e AuditEntry) error { return v.s.addAudit(ctx, e) }
func (v auditView) List(ctx context.Context, limit int, before int64) ([]AuditEntry, error) {
	return v.s.listAudit(ctx, limit, before)
}
