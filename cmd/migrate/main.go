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

func main() {
	var dbURL string
	flag.StringVar(&dbURL, "database-url", "", "PostgreSQL database connection URL (defaults to MINICLOUD_DATABASE_URL)")
	flag.Parse()

	if dbURL == "" {
		dbURL = os.Getenv("MINICLOUD_DATABASE_URL")
	}
	if dbURL == "" {
		slog.Error("database URL must be provided via -database-url or MINICLOUD_DATABASE_URL")
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
