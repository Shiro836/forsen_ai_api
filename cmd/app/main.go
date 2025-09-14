package main

import (
	"context"
	_ "embed"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"time"

	"app/cfg"
	"app/db"
	"app/internal/app/api"
	"app/internal/app/conns"
	"app/internal/app/notifications"
	"app/internal/app/nvidia"
	"app/internal/app/processor"
	"app/pkg/ai"
	"app/pkg/ffmpeg"
	"app/pkg/llm"
	"app/pkg/s3client"
	"app/pkg/twitch"
	"app/pkg/whisperx"

	influxdb2 "github.com/influxdata/influxdb-client-go/v2"
	"github.com/prometheus/client_golang/prometheus"
	"gopkg.in/yaml.v3"
)

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

	httpClient := &http.Client{
		Timeout: 30 * time.Second,
	}

	influxDBClient := influxdb2.NewClient(cfg.InfluxDB.URL, cfg.InfluxDB.Token)

	influxCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if ok, err := influxDBClient.Ping(influxCtx); err != nil {
		log.Fatal("failed to ping influxdb", err)
	} else if !ok {
		log.Fatal("failed to ping influxdb")
	}

	influxWriter := influxDBClient.WriteAPI(cfg.InfluxDB.Org, cfg.InfluxDB.Bucket)
	defer influxWriter.Flush()

	// logger := slog.New(&slg.InfluxDBHandler{InfluxDBWriter: influxWriter})
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	slog.SetDefault(logger)

	reg := prometheus.NewRegistry()
	nvidia.RegisterMetrics(reg)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	llmModel := llm.New(httpClient, &cfg.LLM)
	imageLlm := llm.New(httpClient, &cfg.ImageLLM)
	styleTts := ai.NewStyleTTSClient(httpClient, &cfg.StyleTTS)
	whisper := whisperx.New(httpClient, &cfg.Whisper)
	ffmpeg := ffmpeg.New(&cfg.Ffmpeg)

	// init s3 client
	s3, err := s3client.New(ctx, &cfg.S3)
	if err != nil {
		log.Fatal("failed to init s3 client: ", err)
	}

	controlPanelNotifications := notifications.New()

	processor := processor.NewProcessor(logger.WithGroup("processor"), llmModel, imageLlm, styleTts, whisper, db, ffmpeg, controlPanelNotifications, s3)

	connManager := conns.NewConnectionManager(ctx, logger.WithGroup("conns"), processor)

	twitchClient := twitch.New(httpClient, &cfg.Twitch)

	api := api.NewAPI(&cfg.Api, logger.WithGroup("api"), connManager, controlPanelNotifications, twitchClient, styleTts, llmModel, db, s3)

	router := api.NewRouter()

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

	loop:
		for {
			select {
			case <-time.After(5 * time.Second):
			case <-ctx.Done():
				break loop
			}

			nvidia.GatherAndSendMetrics(ctx, reg, influxWriter, logger.WithGroup("metrics"))
		}
	}()

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

		if err := ProcessingLoop(ctx, logger.WithGroup("exec_loop"), db, connManager); err != nil {
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
				if err := db.CleanQueue(ctx); err != nil {
					logger.Error("failed to clean db msg queue", "err", err)
				}
			case <-ctx.Done():
				ticker.Stop()
				break loop
			}
		}
	}()

	// wg.Add(1)
	// go func() {
	// 	defer wg.Done()

	// 	nvidia.MonitoringLoop(ctx, logger.WithGroup("nvidia"))
	// }()

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

func ProcessingLoop(ctx context.Context, logger *slog.Logger, dbObj *db.DB, cm *conns.Manager) error {
	var users []*db.User
	var err error

	for len(users) == 0 {
		users, err = dbObj.GetUsersPermissions(ctx, db.PermissionStreamer, db.PermissionStatusGranted)
		if err != nil {
			return fmt.Errorf("failed to get whitelist: %w", err)
		}

		if len(users) == 0 {
			logger.Info("no users in whitelist, waiting...")
			select {
			case <-time.After(time.Second):
			case <-ctx.Done():
				return nil
			}
		}
	}

	logger.Info("got users from db", "users", users)

	for _, user := range users {
		cm.HandleUser(user)
	}

	cm.Wait()

	return nil
}
