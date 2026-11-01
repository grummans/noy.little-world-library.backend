// Command migrate runs Goose migrations against DATABASE_URL.
//
//	go run ./cmd/migrate up|down|reset|status
package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"time"

	"littleworld/internal/config"
	"littleworld/internal/db"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "migrate:", err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) != 2 {
		return fmt.Errorf("usage: migrate up|down|reset|status")
	}
	cmd := os.Args[1]

	cfg := config.Load()
	if cfg.DatabaseURL == "" {
		return fmt.Errorf("DATABASE_URL is empty")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	pool, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	switch cmd {
	case "up":
		return db.Up(ctx, pool)
	case "down":
		if err := guardDestructive(ctx, pool); err != nil {
			return err
		}
		return db.Down(ctx, pool)
	case "reset":
		if err := guardDestructive(ctx, pool); err != nil {
			return err
		}
		return db.Reset(ctx, pool)
	case "status":
		return db.Status(ctx, pool)
	default:
		return fmt.Errorf("unknown command %q", cmd)
	}
}

// guardDestructive keeps `down`/`reset` from touching production by accident.
func guardDestructive(ctx context.Context, pool *sql.DB) error {
	var name string
	if err := pool.QueryRowContext(ctx, "SELECT current_database()").Scan(&name); err != nil {
		return fmt.Errorf("check database name: %w", err)
	}
	if name != "little_world_dev" && os.Getenv("ALLOW_DESTRUCTIVE_MIGRATIONS") != "1" {
		return fmt.Errorf("refusing to roll back database %q (set ALLOW_DESTRUCTIVE_MIGRATIONS=1 if intended)", name)
	}
	return nil
}
