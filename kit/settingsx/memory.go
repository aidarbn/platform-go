package settingsx

import (
	"context"
	"maps"
	"sync"
)

// MemoryRepo keeps overrides in memory. Tests of project code use it so that reading a
// setting needs no database.
type MemoryRepo struct {
	mu     sync.Mutex
	values map[string]string
}

// NewMemoryRepo creates a repository with the given overrides.
func NewMemoryRepo(values map[string]string) *MemoryRepo {
	return &MemoryRepo{values: maps.Clone(values)}
}

// Load returns the stored overrides.
func (r *MemoryRepo) Load(context.Context) (map[string]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.values == nil {
		return map[string]string{}, nil
	}
	return maps.Clone(r.values), nil
}

// Save stores an override.
func (r *MemoryRepo) Save(_ context.Context, key, value, _ string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.values == nil {
		r.values = make(map[string]string)
	}
	r.values[key] = value
	return nil
}

// Delete removes an override.
func (r *MemoryRepo) Delete(_ context.Context, key string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.values, key)
	return nil
}

// NewTestStore builds a store on a memory repository with the overrides already loaded.
func NewTestStore(schema Schema, overrides map[string]string) *Store {
	s := NewStore(schema, NewMemoryRepo(overrides), nil)
	if err := s.Reload(context.Background()); err != nil {
		panic("settingsx: test store: " + err.Error())
	}
	return s
}
