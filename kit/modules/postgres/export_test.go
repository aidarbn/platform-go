package postgres

import "github.com/aidarbn/platform-go/kit/pgdb"

// PoolConfig exposes the settings the module would open the pool with.
func (m *Module) PoolConfig() pgdb.Config { return m.poolConfig() }
