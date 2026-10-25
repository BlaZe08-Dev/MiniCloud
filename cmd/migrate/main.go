package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BlaZe08-Dev/Minicloud/internal/storage/postgres"
)

// ResolveDatabaseURL resolves the database URL primarily from the MINICLOUD_DATABASE_URL
// environment variable. If the deprecated -database-url flag is provided, a deprecation warning
// is emitted stating that command-line flags expose credentials in the system process table.
func ResolveDatabaseURL(flagURL string, getenv func(string) string, warnLogger func(string, ...any)) (string, error) {
	envURL := getenv("MINICLOUD_DATABASE_URL")
	if flagURL != "" {
		warnLogger("DEPRECATION WARNING: passing database URL via -database-url exposes credentials in the system process table; use MINICLOUD_DATABASE_URL environment variable instead")
	}

	url := envURL
	if url == "" {
		url = flagURL
	}
	if url == "" {
		return "", fmt.Errorf("database URL must be provided via MINICLOUD_DATABASE_URL environment variable")
	}
	return url, nil
}

func main() {
	var flagDBURL string
	flag.StringVar(&flagDBURL, "database-url", "", "[DEPRECATED] PostgreSQL database connection URL (use MINICLOUD_DATABASE_URL environment variable)")
	flag.Parse()

	dbURL, err := ResolveDatabaseURL(flagDBURL, os.Getenv, func(msg string, args ...any) {
		slog.Warn(msg, args...)
	})
	if err != nil {
		slog.Error("configuration error", "err", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	conn, err := pgx.Connect(ctx, dbURL)
	if err != nil {
		slog.Error("failed to connect to postgres", "err", err)
		os.Exit(1)
	}
	defer conn.Close(ctx)

	migrator := postgres.NewMigrator()
	applied, err := migrator.Migrate(ctx, conn)
	if err != nil {
		slog.Error("migration failed", "err", err)
		os.Exit(1)
	}

	if applied == 0 {
		fmt.Println("Database is already up to date. 0 migrations applied.")
	} else {
		fmt.Printf("Successfully applied %d migration(s).\n", applied)
	}
}
