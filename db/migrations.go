package db

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"
)

// Migrations holds the postgres schema, numbered and never edited; every file
// is written IF NOT EXISTS so applying the whole set is idempotent.
//
//go:embed migrations/*.sql
var Migrations embed.FS

const MigrationsDir = "migrations"

// ClickHouseMigrations holds the archive DDL (adr/history-archive.md §3),
// applied by cmd/migrator. Numbered, never edited.
//
//go:embed ch_migrations/*.sql
var ClickHouseMigrations embed.FS

const ClickHouseMigrationsDir = "ch_migrations"

// ApplyMigrations runs every *.sql file under dir of fsys in name order,
// statement by statement: CREATE INDEX CONCURRENTLY cannot run inside a
// transaction, so files are never sent as one batch. log, when non-nil, is
// told each file before it runs.
func (db *DB) ApplyMigrations(ctx context.Context, fsys fs.FS, dir string, log func(string)) error {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return fmt.Errorf("read migrations: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	for _, name := range names {
		content, err := fs.ReadFile(fsys, dir+"/"+name)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}
		if log != nil {
			log(name)
		}
		for _, stmt := range strings.Split(string(content), ";") {
			stmt = strings.TrimSpace(stmt)
			if stmt == "" {
				continue
			}
			if _, err := db.Exec(ctx, stmt); err != nil {
				return fmt.Errorf("migration %s: %w", name, err)
			}
		}
	}
	return nil
}
