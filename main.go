package main

import (
	"app/ai_clients/llm"
	"app/ai_clients/rvc"
	"app/ai_clients/tts"
	"app/api"
	"app/conns"
	"app/db"
	"app/metrics"
	"app/pkg/slg"
	"app/processor"
	"app/twitch"
	"flag"
	"strconv"

	"app/cfg"
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

	var cfgPath string
	flag.StringVar(&cfgPath, "cfg", "cfg/cfg.yaml", "path to config file")
	flag.Parse()

	var cfg *cfg.Config
	if cfgFile, err := os.ReadFile(cfgPath); err != nil {
		log.Fatalf("can't open %s file: %v", cfgPath, err)
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

	logger := slog.New(slog.NewTextHandler(logFile, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	}))

	slog.SetDefault(logger)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ctx = slg.WithSlog(ctx, logger)

	rvc := rvc.New(httpClient, &cfg.Rvc)
	llm := llm.New(httpClient, &cfg.LLM)
	tts := tts.New(httpClient, &cfg.TTS)

	processor := processor.NewProcessor(logger.WithGroup("processor"), llm, tts, rvc, nil)

	connManager := conns.NewConnectionManager(ctx, logger.WithGroup("conns"), processor)

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

		logger.Info("Starting server")

		if err := srv.ListenAndServe(); err != nil {
			logger.Error("ListenAndServe finished", "err", err)
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		defer cancel()

		logger.Info("Starting connections loop")

		if err := ProcessingLoop(ctx, connManager); err != nil {
			logger.Error("Processing loop error", "err", err)
		}

		logger.Info("Connections loop finished")
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
					logger.Error("failed to clean db msg queue", "err", err)
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

		metrics.NvidiaMonitoringLoop(ctx, logger.WithGroup("nvidia"))
	}()

	select {
	case <-ctx.Done():
	case <-stop:
		logger.Info("Interrupt triggerred")
		cancel()
	}

	if err := srv.Shutdown(ctx); err != nil {
		log.Fatal(err)
	}

	wg.Wait()
}
