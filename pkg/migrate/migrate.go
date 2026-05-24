// Package migrate wraps golang-migrate with hash verification and structured logging.
// All migrations are forward-only in production; .down.sql files exist only for dev/staging.
package migrate

import (
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"strings"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	_ "github.com/lib/pq"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// FS is an embed.FS populated by the caller (typically scripts/migrate/main.go) with
// the migrations directory contents. Declared here so pkg/migrate is import-only.
var FS embed.FS

// Config controls migration behaviour.
type Config struct {
	// DatabaseURL is the postgres DSN, e.g. postgres://user:pass@host/dbname?sslmode=disable
	DatabaseURL string
	// MigrationsTable overrides the default schema_migrations table name.
	MigrationsTable string
	// AllowDown permits running down migrations. Must be false in production.
	AllowDown bool
	// HashManifestPath is the path to a file containing expected SHA-256 hashes of each
	// migration file. The frozen-check CI gate writes this file; Up() verifies it.
	// Empty string disables the check (useful in dev).
	HashManifestPath string
}

// Runner applies migrations using golang-migrate.
type Runner struct {
	cfg    Config
	db     *sql.DB
	logger zerolog.Logger
}

// New opens the database connection and returns a Runner ready to apply migrations.
func New(cfg Config, migrationsFS fs.FS) (*Runner, error) {
	if cfg.MigrationsTable == "" {
		cfg.MigrationsTable = "schema_migrations"
	}

	db, err := sql.Open("postgres", cfg.DatabaseURL)
	if err != nil {
		return nil, fmt.Errorf("migrate: open db: %w", err)
	}
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("migrate: ping db: %w", err)
	}

	return &Runner{cfg: cfg, db: db, logger: log.With().Str("component", "migrate").Logger()}, nil
}

// Up applies all pending migrations after verifying file hashes.
func (r *Runner) Up(migrationsFS fs.FS) error {
	if r.cfg.HashManifestPath != "" {
		if err := r.verifyHashes(migrationsFS); err != nil {
			return fmt.Errorf("migrate: hash verification failed: %w", err)
		}
	}

	m, err := r.newMigrate(migrationsFS)
	if err != nil {
		return err
	}
	defer m.Close()

	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("migrate: up: %w", err)
	}

	v, dirty, _ := m.Version()
	r.logger.Info().Uint("version", v).Bool("dirty", dirty).Msg("migrations applied")
	return nil
}

// Down runs all down migrations. Panics if AllowDown is false — callers must never
// call this in production code paths.
func (r *Runner) Down(migrationsFS fs.FS) error {
	if !r.cfg.AllowDown {
		panic("migrate: down migrations are disabled; set AllowDown=true for dev/staging only")
	}

	m, err := r.newMigrate(migrationsFS)
	if err != nil {
		return err
	}
	defer m.Close()

	if err := m.Down(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("migrate: down: %w", err)
	}
	return nil
}

// Status prints the current migration version and whether the DB is dirty.
func (r *Runner) Status(migrationsFS fs.FS) error {
	m, err := r.newMigrate(migrationsFS)
	if err != nil {
		return err
	}
	defer m.Close()

	v, dirty, err := m.Version()
	if errors.Is(err, migrate.ErrNilVersion) {
		r.logger.Info().Msg("no migrations applied yet")
		return nil
	}
	if err != nil {
		return fmt.Errorf("migrate: version: %w", err)
	}
	r.logger.Info().Uint("version", v).Bool("dirty", dirty).Msg("migration status")
	return nil
}

// Close closes the underlying database connection.
func (r *Runner) Close() error {
	return r.db.Close()
}

// WriteHashManifest computes SHA-256 hashes for all .up.sql files and writes a
// manifest to path. Called by CI when a migration is first merged.
func WriteHashManifest(migrationsFS fs.FS, manifestPath string) error {
	hashes, err := computeHashes(migrationsFS)
	if err != nil {
		return err
	}

	var sb strings.Builder
	names := make([]string, 0, len(hashes))
	for name := range hashes {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		fmt.Fprintf(&sb, "%s  %s\n", hashes[name], name)
	}

	return os.WriteFile(manifestPath, []byte(sb.String()), 0o644)
}

// verifyHashes checks that no previously-merged migration file has been edited.
func (r *Runner) verifyHashes(migrationsFS fs.FS) error {
	data, err := os.ReadFile(r.cfg.HashManifestPath)
	if os.IsNotExist(err) {
		r.logger.Warn().Str("path", r.cfg.HashManifestPath).Msg("hash manifest missing; skipping verification")
		return nil
	}
	if err != nil {
		return fmt.Errorf("read hash manifest: %w", err)
	}

	expected := parseManifest(string(data))
	actual, err := computeHashes(migrationsFS)
	if err != nil {
		return err
	}

	for name, expHash := range expected {
		actHash, exists := actual[name]
		if !exists {
			return fmt.Errorf("migration file %q is in manifest but missing from disk", name)
		}
		if actHash != expHash {
			return fmt.Errorf("migration file %q has been modified (manifest: %s, disk: %s)", name, expHash, actHash)
		}
	}
	return nil
}

func (r *Runner) newMigrate(migrationsFS fs.FS) (*migrate.Migrate, error) {
	src, err := iofs.New(migrationsFS, ".")
	if err != nil {
		return nil, fmt.Errorf("migrate: create iofs source: %w", err)
	}

	driver, err := postgres.WithInstance(r.db, &postgres.Config{
		MigrationsTable: r.cfg.MigrationsTable,
		DatabaseName:    "governance",
	})
	if err != nil {
		return nil, fmt.Errorf("migrate: create postgres driver: %w", err)
	}

	m, err := migrate.NewWithInstance("iofs", src, "postgres", driver)
	if err != nil {
		return nil, fmt.Errorf("migrate: init: %w", err)
	}
	m.Log = &migrateLogger{logger: r.logger}
	return m, nil
}

func computeHashes(migrationsFS fs.FS) (map[string]string, error) {
	hashes := make(map[string]string)
	err := fs.WalkDir(migrationsFS, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".up.sql") {
			return err
		}
		data, err := fs.ReadFile(migrationsFS, path)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		hashes[path] = hex.EncodeToString(sum[:])
		return nil
	})
	return hashes, err
}

func parseManifest(content string) map[string]string {
	m := make(map[string]string)
	for _, line := range strings.Split(content, "\n") {
		parts := strings.Fields(line)
		if len(parts) == 2 {
			m[parts[1]] = parts[0]
		}
	}
	return m
}

type migrateLogger struct{ logger zerolog.Logger }

func (l *migrateLogger) Printf(format string, v ...interface{}) {
	l.logger.Info().Msgf(strings.TrimRight(format, "\n"), v...)
}
func (l *migrateLogger) Verbose() bool { return false }
