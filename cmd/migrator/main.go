package main

import (
	"app/cfg"
	"app/db"
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

const drop = true

const migrationsFolder = "db/migrations"

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
	db, err := db.New(createDbCtx, &cfg.DB)
	if err != nil {
		log.Fatal("failed to init postgre db: ", err)
	}

	files, err := os.ReadDir(migrationsFolder)
	if err != nil {
		log.Fatalf("can't read migrations folder: %v", err)
	}

	if drop {
		_, err := db.Exec(context.Background(), dropEverythingQuery)
		if err != nil {
			log.Fatalf("can't drop everything: %v", err)
		}
		log.Println("dropped all tables")
	}

	for _, file := range files {
		filePath := migrationsFolder + "/" + file.Name()
		file, err := os.ReadFile(filePath)
		if err != nil {
			log.Fatalf("can't read file %s: %v", filePath, err)
		}

		log.Printf("applying migration %s", filePath)

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, err = db.Exec(ctx, string(file))
		if err != nil {
			log.Fatalf("can't execute migration %s: %v", filePath, err)
			return
		}
	}

	log.Println("migrations applied")
}
