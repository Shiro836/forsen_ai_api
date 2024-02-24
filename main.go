package main

import (
	"app/ai"
	"app/api"
	"app/conns"
	"app/db"
	"app/metrics"
	"app/rvc"
	"app/slg"
	"app/tts"
	"app/twitch"
	"strconv"

	"context"
	_ "embed"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// var p = bluemonday.StrictPolicy()

// func writeFile(fileName string, data []byte) error {
// 	file, err := os.Create(fileName)
// 	if err != nil {
// 		return fmt.Errorf("failed to create file: %w", err)
// 	}
// 	defer file.Close()

// 	_, err = file.Write(data)
// 	if err != nil {
// 		return fmt.Errorf("failed to create file: %w", err)
// 	}

// 	return nil
// }

func main() {
	db.InitDB()
	defer db.Close()

	var cfg *Config
	if cfgFile, err := os.ReadFile("cfg.yaml"); err != nil {
		log.Fatal("can't open cfg.yaml file", err)
	} else if err = yaml.Unmarshal(cfgFile, &cfg); err != nil {
		log.Fatal("can't unmarshal cfg.yaml file", err)
	}

	// createDbCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	// postgreDB, err := postgredb.New(createDbCtx, &postgredb.Config{
	// 	ConnStr: cfg.DB.ConnStr,
	// })
	// if err != nil {
	// 	log.Fatal("failed to init postgre db", err)
	// }

	// postgreDB.Test()

	httpClient := &http.Client{
		Timeout: 30 * time.Second,
	}

	logFile, err := os.OpenFile("logs/log.txt", os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o600)
	if err != nil {
		log.Fatal(err)
	}

	slogLogger := slog.New(slog.NewTextHandler(logFile, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))

	slog.SetDefault(slogLogger)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ctx = slg.WithSlog(ctx, slogLogger)

	rvc := rvc.New(httpClient, &cfg.Rvc)
	ai := ai.New(httpClient, &cfg.AI)
	tts := tts.New(httpClient, &cfg.TTS)

	processor := NewProcessor(slogLogger.With("processor"), ai, tts, rvc, nil)

	connManager := conns.NewConnectionManager(ctx, processor)

	twitchClient := twitch.New(httpClient, &cfg.Twitch)

	apiClient := api.NewAPI(connManager, twitchClient, tts)

	router := api.NewRouter(apiClient)

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt)

	srv := &http.Server{
		Addr:           ":" + strconv.Itoa(cfg.Api.Port),
		Handler:        router,
		MaxHeaderBytes: 20971520,
	}

	wg := sync.WaitGroup{}

	wg.Add(1)
	go func() {
		defer wg.Done()
		defer cancel()

		slog.Info("Starting server")

		if err := srv.ListenAndServe(); err != nil {
			slog.Error("ListenAndServe finished", "err", err)
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		defer cancel()

		slog.Info("Starting connections loop")

		if err := ProcessingLoop(ctx, connManager); err != nil {
			slog.Error("Processing loop error", "err", err)
		}

		slog.Info("Connections loop finished")
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()

		ticker := time.NewTicker(30 * time.Minute)

	loop:
		for {
			select {
			case <-ticker.C:
				if err := db.CleanQueue(); err != nil {
					slogLogger.Error("failed to clean db msg queue", "err", err)
				}
			case <-ctx.Done():
				ticker.Stop()
				break loop
			}
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()

		metrics.NvidiaMonitoringLoop(ctx)
	}()

	select {
	case <-ctx.Done():
	case <-stop:
		slog.Info("Interrupt triggerred")
		cancel()
	}

	if err := srv.Shutdown(ctx); err != nil {
		log.Fatal(err)
	}

	wg.Wait()
}
