package postgredb

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"sort"

	"github.com/jackc/pgx/v5/pgxpool"
)

type DB struct {
	db *pgxpool.Pool
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

	if err = db.migrate(ctx); err != nil {
		return nil, fmt.Errorf("migration err: %w", err)
	}

	return db, nil
}

func (db *DB) migrate(ctx context.Context) error {
	folder := "postgre_db/migrations"

	migrations, err := os.ReadDir(folder)
	if err != nil {
		return fmt.Errorf("failed to read dir: %w", err)
	}

	files := []string{}
	for _, migration := range migrations {
		files = append(files, migration.Name())
	}

	sort.Strings(files)

	for _, file := range files {
		f, err := os.Open(folder + "/" + file)
		if err != nil {
			log.Fatal(err)
		}

		data, err := io.ReadAll(f)
		if err != nil {
			log.Fatal(err)
		}

		fmt.Println("applying", file)

		_, err = db.db.Exec(ctx, string(data))
		if err != nil {
			return fmt.Errorf("failed to apply file: %s, err: %w", file, err)
		}
	}

	return nil
}

func (db *DB) Test() {

}
