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
		log.Fatal("failed to init postgre db", err)
	}

	files, err := os.ReadDir(migrationsFolder)
	if err != nil {
		log.Fatalf("can't read migrations folder: %v", err)
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
		_, err = db.RawConn().Exec(ctx, string(file))
		if err != nil {
			log.Fatalf("can't execute migration %s: %v", filePath, err)
			return
		}
	}

	log.Println("migrations applied")
}
