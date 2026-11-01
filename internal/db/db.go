// Package db owns the PostgreSQL connection pool and the embedded Goose migrations.
package db

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

//go:embed migrations/*.sql
var migrations embed.FS

func Open(ctx context.Context, dsn string) (*sql.DB, error) {
	pool, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	// Unbounded pools exhaust PostgreSQL connections under load; these bounds are
	// well under the default max_connections (100).
	pool.SetMaxOpenConns(10)
	pool.SetMaxIdleConns(5)
	pool.SetConnMaxLifetime(30 * time.Minute)
	pool.SetConnMaxIdleTime(5 * time.Minute)
	if err := pool.PingContext(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return pool, nil
}

// Up brings the target database to the latest migration.
func Up(ctx context.Context, pool *sql.DB) error {
	return run(ctx, pool, goose.UpContext)
}

// Down rolls back a single migration.
func Down(ctx context.Context, pool *sql.DB) error {
	return run(ctx, pool, goose.DownContext)
}

// Reset rolls back every migration; it is for disposable databases only.
func Reset(ctx context.Context, pool *sql.DB) error {
	return run(ctx, pool, func(ctx context.Context, db *sql.DB, dir string, _ ...goose.OptionsFunc) error {
		return goose.DownToContext(ctx, db, dir, 0)
	})
}

func Status(ctx context.Context, pool *sql.DB) error {
	return run(ctx, pool, func(ctx context.Context, db *sql.DB, dir string, _ ...goose.OptionsFunc) error {
		return goose.StatusContext(ctx, db, dir)
	})
}

func run(ctx context.Context, pool *sql.DB, fn func(context.Context, *sql.DB, string, ...goose.OptionsFunc) error) error {
	goose.SetBaseFS(migrations)
	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("set goose dialect: %w", err)
	}
	if err := fn(ctx, pool, "migrations"); err != nil {
		return fmt.Errorf("run migrations: %w", err)
	}
	return nil
}
