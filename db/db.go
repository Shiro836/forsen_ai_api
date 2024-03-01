package db

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

type DB struct {
	db *pgxpool.Pool
}

func (db *DB) RawConn() *pgxpool.Pool {
	return db.db
}

type Config struct {
	ConnStr string `yaml:"conn_str"`
}

func New(ctx context.Context, cfg *Config) (*DB, error) {
	pool, err := pgxpool.New(ctx, cfg.ConnStr)
	if err != nil {
		return nil, fmt.Errorf("failed to create db pool: %w", err)
	}

	if err = pool.Ping(ctx); err != nil {
		return nil, fmt.Errorf("failed to ping db: %w", err)
	}

	db := &DB{
		db: pool,
	}

	return db, nil
}
