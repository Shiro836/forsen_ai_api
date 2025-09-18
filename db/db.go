package db

import (
	"context"
	"fmt"
	"io"

	"github.com/jackc/pgx/v5/pgxpool"
)

type DB struct {
	*pgxpool.Pool
	s3 S3Provider
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

	db := &DB{Pool: pool}

	return db, nil
}

// S3Provider is the subset of s3 client used by DB
type S3Provider interface {
	EnsureBucket(ctx context.Context, bucket string) error
	PutObject(ctx context.Context, bucket string, objectName string, reader io.Reader, size int64, contentType string) error
	GetObject(ctx context.Context, bucket string, objectName string) (io.ReadCloser, error)
}

// AttachS3Client injects an S3 client into DB for media storage
func (db *DB) AttachS3Client(s3 S3Provider) {
	db.s3 = s3
}
