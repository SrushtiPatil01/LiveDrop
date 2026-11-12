package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/SrushtiPatil01/LiveDrop/internal/store/postgres"
)

func main() {
	if err := run(); err != nil {
		slog.Error("database migration failed", "error", err)
		os.Exit(1)
	}
}

func run() error {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		return fmt.Errorf("DATABASE_URL is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store, err := postgres.Open(ctx, databaseURL)
	if err != nil {
		return err
	}
	defer store.Close()
	if err := store.Migrate(ctx); err != nil {
		return fmt.Errorf("migrate database: %w", err)
	}
	slog.Info("database migrations applied")
	return nil
}
