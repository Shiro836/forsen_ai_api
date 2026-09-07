// Package clickhouse opens the archive store connection (clickhouse-go v2,
// native protocol) and applies the embedded DDL for cmd/migrator.
package clickhouse

import (
	"context"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

type Config struct {
	// Addr is the native-protocol host:port; empty disables the archive.
	Addr     string `yaml:"addr"`
	User     string `yaml:"user"`
	Password string `yaml:"password"`
	Database string `yaml:"database"`

	Timeout time.Duration `yaml:"timeout"`
}

// Open connects to the configured database and pings it. Returns nil, nil
// when cfg.Addr is empty. The schema is applied by cmd/migrator, not here.
func Open(ctx context.Context, cfg *Config) (driver.Conn, error) {
	if cfg == nil || cfg.Addr == "" {
		return nil, nil
	}
	conn, err := clickhouse.Open(options(cfg, cfg.Database))
	if err != nil {
		return nil, fmt.Errorf("clickhouse: open: %w", err)
	}
	if err := conn.Ping(ctx); err != nil {
		conn.Close()
		return nil, fmt.Errorf("clickhouse: ping: %w", err)
	}
	return conn, nil
}

func options(cfg *Config, database string) *clickhouse.Options {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &clickhouse.Options{
		Addr:        []string{cfg.Addr},
		Auth:        clickhouse.Auth{Database: database, Username: cfg.User, Password: cfg.Password},
		DialTimeout: timeout,
		ReadTimeout: timeout,
		Compression: &clickhouse.Compression{Method: clickhouse.CompressionLZ4},
		Settings: clickhouse.Settings{
			// the archive reads through FINAL; every row of a message lives
			// in one month partition, so cross-partition merging is wasted work
			"do_not_merge_across_partitions_select_final": 1,
		},
	}
}

// Migrate creates the configured database if missing and applies every *.sql
// file under dir of fsys in name order, one statement per file. Files are
// written IF NOT EXISTS so re-running is a no-op, the postgres migrator's
// contract.
func Migrate(ctx context.Context, cfg *Config, fsys fs.FS, dir string) error {
	if cfg.Database != "" {
		admin, err := clickhouse.Open(options(cfg, ""))
		if err != nil {
			return fmt.Errorf("clickhouse: open: %w", err)
		}
		err = admin.Exec(ctx, "CREATE DATABASE IF NOT EXISTS "+cfg.Database)
		admin.Close()
		if err != nil {
			return fmt.Errorf("clickhouse: create database: %w", err)
		}
	}

	conn, err := clickhouse.Open(options(cfg, cfg.Database))
	if err != nil {
		return fmt.Errorf("clickhouse: open: %w", err)
	}
	defer conn.Close()

	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return fmt.Errorf("clickhouse: read migrations: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		stmt, err := fs.ReadFile(fsys, dir+"/"+name)
		if err != nil {
			return fmt.Errorf("clickhouse: read %s: %w", name, err)
		}
		if err := conn.Exec(ctx, string(stmt)); err != nil {
			return fmt.Errorf("clickhouse: migration %s: %w", name, err)
		}
	}
	return nil
}
