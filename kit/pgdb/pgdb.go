// Package pgdb создаёт пул соединений к PostgreSQL и даёт помощники для транзакций.
//
// Пакет намеренно тонкий: запросы пишутся генераторами (sqlc для статических,
// jet для динамических), а здесь только то, что одинаково во всех проектах.
package pgdb

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Config — параметры пула. Пустые поля берут значения по умолчанию.
type Config struct {
	URL             string
	MaxConns        int32
	MinConns        int32
	MaxConnLifetime time.Duration
	MaxConnIdleTime time.Duration
	ConnectTimeout  time.Duration
}

func (c *Config) setDefaults() {
	if c.MaxConns == 0 {
		c.MaxConns = 10
	}
	if c.MaxConnLifetime == 0 {
		c.MaxConnLifetime = time.Hour
	}
	if c.MaxConnIdleTime == 0 {
		c.MaxConnIdleTime = 30 * time.Minute
	}
	if c.ConnectTimeout == 0 {
		c.ConnectTimeout = 5 * time.Second
	}
}

// Open создаёт пул и проверяет соединение: без проверки приложение поднимется
// с нерабочей базой и упадёт позже, на первом запросе пользователя.
func Open(ctx context.Context, cfg Config) (*pgxpool.Pool, error) {
	cfg.setDefaults()

	if cfg.URL == "" {
		return nil, errors.New("pgdb: адрес базы не задан")
	}

	poolCfg, err := pgxpool.ParseConfig(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("pgdb: разбор адреса базы: %w", err)
	}
	poolCfg.MaxConns = cfg.MaxConns
	poolCfg.MinConns = cfg.MinConns
	poolCfg.MaxConnLifetime = cfg.MaxConnLifetime
	poolCfg.MaxConnIdleTime = cfg.MaxConnIdleTime
	poolCfg.ConnConfig.ConnectTimeout = cfg.ConnectTimeout

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("pgdb: создание пула: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, cfg.ConnectTimeout)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("pgdb: база недоступна: %w", err)
	}
	return pool, nil
}

// InTx выполняет fn в транзакции: при ошибке или панике откатывает, иначе фиксирует.
func InTx(ctx context.Context, pool *pgxpool.Pool, fn func(tx pgx.Tx) error) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("pgdb: начало транзакции: %w", err)
	}

	committed := false
	defer func() {
		if committed {
			return
		}
		// Откат по своему контексту: контекст запроса может быть уже отменён,
		// а соединение всё равно надо вернуть в пул чистым.
		rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(rollbackCtx)
	}()

	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("pgdb: фиксация транзакции: %w", err)
	}
	committed = true
	return nil
}
