package main

import (
	"app/cfg"
	"app/db"
	"app/pkg/clickhouse"
	"context"
	"flag"
	"log"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

const dropEverythingQuery = `
	DROP TABLE if exists relations;
	DROP TABLE if exists reward_buttons;
	DROP TABLE if exists msg_queue;
	DROP TABLE if exists permissions;
	DROP TABLE if exists history;
	DROP TABLE if exists char_cards;
	DROP TABLE if exists users;
`

const drop = false

func main() {
	var cfgPath string
	flag.StringVar(&cfgPath, "cfg-path", "cfg/cfg.yaml", "path to config file")
	flag.Parse()

	var cfg *cfg.Config
	if cfgFile, err := os.ReadFile(cfgPath); err != nil {
		log.Fatalf("can't open %s file: %v", cfgPath, err)
	} else if err = yaml.Unmarshal(cfgFile, &cfg); err != nil {
		log.Fatal("can't unmarshal cfg.yaml file", err)
	}

	createDbCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pg, err := db.New(createDbCtx, &cfg.DB)
	if err != nil {
		log.Fatal("failed to init postgre db: ", err)
	}

	if drop {
		_, err := pg.Exec(context.Background(), dropEverythingQuery)
		if err != nil {
			log.Fatalf("can't drop everything: %v", err)
		}
		log.Println("dropped all tables")
	}

	migrateCtx, cancelMigrate := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancelMigrate()
	if err := pg.ApplyMigrations(migrateCtx, db.Migrations, db.MigrationsDir, func(name string) {
		log.Printf("applying migration %s", name)
	}); err != nil {
		log.Fatalf("can't apply migrations: %v", err)
	}

	log.Println("migrations applied")

	if cfg.ClickHouse.Addr == "" {
		log.Println("clickhouse not configured, skipping archive migrations")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := clickhouse.Migrate(ctx, &cfg.ClickHouse, db.ClickHouseMigrations, db.ClickHouseMigrationsDir); err != nil {
		log.Fatalf("can't apply clickhouse migrations: %v", err)
	}
	log.Println("clickhouse migrations applied")
}
