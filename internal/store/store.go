// Package store is the persistence layer: Postgres (authoritative state)
// via pgx, and Redis (pub/sub, rate limits, locks, caches) via go-redis.
// Postgres is the source of truth; everything in Redis is reconstructible.
package store

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/OverlordLoader/emberfall-server/internal/sim"
)

// Store bundles the Postgres pool and the Redis client.
type Store struct {
	DB    *pgxpool.Pool
	Redis *redis.Client
}

// New connects to Postgres and Redis.
func New(ctx context.Context, pgURL, redisURL string) (*Store, error) {
	pgCfg, err := pgxpool.ParseConfig(pgURL)
	if err != nil {
		return nil, fmt.Errorf("parse pg url: %w", err)
	}
	pgCfg.MaxConns = 25
	db, err := pgxpool.NewWithConfig(ctx, pgCfg)
	if err != nil {
		return nil, fmt.Errorf("connect pg: %w", err)
	}
	if err := db.Ping(ctx); err != nil {
		return nil, fmt.Errorf("ping pg: %w", err)
	}
	rdb := redis.NewClient(&redis.Options{Addr: redisURL})
	if err := rdb.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("ping redis: %w", err)
	}
	return &Store{DB: db, Redis: rdb}, nil
}

func (s *Store) Close() {
	s.DB.Close()
	_ = s.Redis.Close()
}

// RunMigrations applies embedded golang-migrate-style SQL migrations in order.
// We use our own tiny runner over the same file convention (up files) so the
// single binary has no external migrate CLI dependency at container start.
// Down migrations are kept for operators who need rollback via the CLI.
func (s *Store) RunMigrations(ctx context.Context) error {
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("read migrations: %w", err)
	}
	if _, err := s.DB.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}
	// Collect *.up.sql in version order.
	var versions []string
	for _, e := range entries {
		n := e.Name()
		if strings.HasSuffix(n, ".up.sql") {
			versions = append(versions, n[:len(n)-len(".up.sql")])
		}
	}
	sort.Strings(versions)
	for _, version := range versions {
		var exists bool
		if err := s.DB.QueryRow(ctx, `SELECT true FROM schema_migrations WHERE version=$1`, version).Scan(&exists); err == nil && exists {
			continue
		}
		name := version + ".up.sql"
		sql, err := migrationsFS.ReadFile("migrations/" + name)
		if err != nil {
			return fmt.Errorf("read %s: %w", name, err)
		}
		tx, err := s.DB.Begin(ctx)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, string(sql)); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("apply %s: %w", name, err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations(version) VALUES($1)`, version); err != nil {
			_ = tx.Rollback(ctx)
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
	}
	return nil
}

// EnsureDefaultWorld creates world 1 (Emberfall — Realm 1) if no worlds exist,
// and makes sure its config JSON is populated from sim.DefaultConfig.
func (s *Store) EnsureDefaultWorld(ctx context.Context) (int, error) {
	var id int
	err := s.DB.QueryRow(ctx, `SELECT id FROM worlds ORDER BY id LIMIT 1`).Scan(&id)
	if err == nil {
		return id, nil
	}
	cfgJSON, _ := json.Marshal(sim.DefaultConfig())
	err = s.DB.QueryRow(ctx,
		`INSERT INTO worlds(name, status, size, max_players, config)
		 VALUES ('Emberfall — Realm 1', 'open', 256, 2500, $1) RETURNING id`, cfgJSON).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("create default world: %w", err)
	}
	return id, nil
}

// WorldConfig loads the data-driven game config for a world.
func (s *Store) WorldConfig(ctx context.Context, worldID int) (sim.GameConfig, error) {
	var raw []byte
	if err := s.DB.QueryRow(ctx, `SELECT config FROM worlds WHERE id=$1`, worldID).Scan(&raw); err != nil {
		return sim.GameConfig{}, err
	}
	var cfg sim.GameConfig
	if len(raw) == 0 || string(raw) == "{}" {
		return sim.DefaultConfig(), nil
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return sim.DefaultConfig(), nil // corrupted config -> safe default, logged by caller
	}
	return cfg, nil
}

// TxFunc runs fn inside a transaction.
func (s *Store) TxFunc(ctx context.Context, fn func(ctx context.Context, tx Querier) error) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(ctx, tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
