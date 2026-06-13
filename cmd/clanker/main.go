package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"app/cfg"
	"app/db"
	"app/internal/app/clanker"
	"app/pkg/llm"

	"gopkg.in/yaml.v3"
)

func main() {
	var cfgPath string
	flag.StringVar(&cfgPath, "cfg-path", "cfg/cfg.yaml", "path to config file")
	flag.Parse()

	var config *cfg.Config
	if cfgFile, err := os.ReadFile(cfgPath); err != nil {
		log.Fatalf("can't open %s file: %v", cfgPath, err)
	} else if err = yaml.Unmarshal(cfgFile, &config); err != nil {
		log.Fatal("can't unmarshal cfg.yaml file", err)
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	createDbCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	database, err := db.New(createDbCtx, &config.DB)
	if err != nil {
		log.Fatal("failed to init postgre db: ", err)
	}
	defer database.Close()

	llmClient := llm.New(http.DefaultClient, &config.AgenticLLM)

	svc, err := clanker.NewService(
		logger.WithGroup("clanker"),
		database,
		llmClient,
		&config.Clanker,
	)
	if err != nil {
		log.Fatal("failed to create clanker service: ", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := svc.Run(ctx); err != nil {
			logger.Error("clanker service failed", "err", err)
			stop <- os.Interrupt
		}
	}()

	if config.Clanker.Port != 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()

			addr := fmt.Sprintf("%s:%d", config.Clanker.Host, config.Clanker.Port)
			mux := http.NewServeMux()
			mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				w.Write([]byte("ok"))
			})

			logger.Info("starting clanker http server", "addr", addr)
			srv := &http.Server{Addr: addr, Handler: mux}

			go func() {
				<-ctx.Done()
				srv.Shutdown(context.Background())
			}()

			if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				logger.Error("clanker http server failed", "err", err)
			}
		}()
	}

	select {
	case <-ctx.Done():
	case <-stop:
		logger.Info("interrupt triggered")
		cancel()
	}

	wg.Wait()
}
