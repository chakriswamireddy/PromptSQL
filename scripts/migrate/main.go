// scripts/migrate/main.go — migration CLI used by `make migrate` and `make migrate-status`.
// Usage: go run scripts/migrate/main.go <up|down|status|hash>
package main

import (
	"embed"
	"fmt"
	"io/fs"
	"os"

	migratelib "github.com/governance-platform/pkg/migrate"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

//go:embed ../../migrations/*.sql
var migrationsFS embed.FS

func main() {
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr})

	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: migrate <up|down|status|hash>")
		os.Exit(1)
	}

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = os.Getenv("POSTGRES_URL")
	}
	if dbURL == "" {
		// Fallback to composed DSN for local dev
		host := envOr("POSTGRES_HOST", "127.0.0.1")
		port := envOr("POSTGRES_PORT", "5432")
		user := envOr("POSTGRES_USER", "app_migration_login")
		pass := envOr("POSTGRES_PASSWORD", "changeme_migration")
		dbname := envOr("POSTGRES_DB", "governance")
		dbURL = fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=disable", user, pass, host, port, dbname)
	}

	sub, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		log.Fatal().Err(err).Msg("failed to create migrations sub-FS")
	}

	cfg := migratelib.Config{
		DatabaseURL:      dbURL,
		AllowDown:        os.Getenv("MIGRATE_ALLOW_DOWN") == "true",
		HashManifestPath: envOr("MIGRATIONS_HASH_MANIFEST", "migrations/hashes.manifest"),
	}

	runner, err := migratelib.New(cfg, sub)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to create migration runner")
	}
	defer runner.Close()

	cmd := os.Args[1]
	switch cmd {
	case "up":
		if err := runner.Up(sub); err != nil {
			log.Fatal().Err(err).Msg("migration up failed")
		}
		log.Info().Msg("migrations complete")

	case "down":
		if os.Getenv("MIGRATE_ALLOW_DOWN") != "true" {
			log.Fatal().Msg("down migrations require MIGRATE_ALLOW_DOWN=true (never set this in production)")
		}
		if err := runner.Down(sub); err != nil {
			log.Fatal().Err(err).Msg("migration down failed")
		}

	case "status":
		if err := runner.Status(sub); err != nil {
			log.Fatal().Err(err).Msg("migration status failed")
		}

	case "hash":
		manifestPath := envOr("MIGRATIONS_HASH_MANIFEST", "migrations/hashes.manifest")
		if err := migratelib.WriteHashManifest(sub, manifestPath); err != nil {
			log.Fatal().Err(err).Msg("hash manifest write failed")
		}
		log.Info().Str("path", manifestPath).Msg("hash manifest written")

	default:
		fmt.Fprintf(os.Stderr, "unknown command %q; use: up, down, status, hash\n", cmd)
		os.Exit(1)
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
