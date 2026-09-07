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
	"syscall"
	"time"

	"app/cfg"
	"app/db"
	"app/internal/app/api"
	"app/internal/app/conns"
	"app/internal/app/history"
	"app/internal/app/monitoring"
	"app/internal/app/processor"
	"app/pkg/agentic"
	"app/pkg/ai"
	"app/pkg/clickhouse"
	"app/pkg/ffmpeg"
	"app/pkg/llm"
	"app/pkg/llmfilter"
	"app/pkg/oai"
	"app/pkg/s3client"
	"app/pkg/twitch"
	"app/pkg/whisperx"

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
		Timeout: 2 * time.Minute,
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	slog.SetDefault(logger)

	monitoring.RegisterMetrics(prometheus.DefaultRegisterer)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	imageLlm := llm.New(httpClient, &cfg.ImageLLM)

	var characterLlm processor.CharacterLLM = llm.ChatClient{Client: llm.New(httpClient, &cfg.LLM2)}
	if cfg.OAI.Timeout <= 0 {
		log.Fatal("oai.timeout is not configured")
	}
	if cfg.FilterLLM.URL == "" || cfg.FilterLLM.Timeout <= 0 {
		log.Fatal("filter_llm url/timeout is not configured")
	}
	oaiClient := oai.New(&cfg.OAI)
	textFilter := llmfilter.New(oai.New(&cfg.FilterLLM))
	ffmpegClient := ffmpeg.New(&cfg.Ffmpeg)
	chatTTSEngine := ai.NewStyleTTSClient(httpClient, &cfg.StyleTTS)
	indexClient := ai.NewIndexTTSClient(httpClient, &cfg.IndexTTS)
	ttsEngine := ai.NewIndexTTSEngine(indexClient, ffmpegClient)
	whisper := whisperx.New(httpClient, &cfg.Whisper)

	s3, err := s3client.New(ctx, &cfg.S3)
	if err != nil {
		log.Fatal("failed to init s3 client: ", err)
	}

	if err := s3.EnsureBucket(ctx, s3client.UserImagesBucket); err != nil {
		log.Fatal("failed to ensure s3 bucket: ", err)
	}
	if err := s3.EnsureBucket(ctx, s3client.CharDataBucket); err != nil {
		log.Fatal("failed to ensure s3 char data bucket: ", err)
	}
	if err := s3.EnsureBucket(ctx, s3client.TTSArchiveBucket); err != nil {
		log.Fatal("failed to ensure s3 tts archive bucket: ", err)
	}

	// attach s3 to db so it can transparently store media
	db.AttachS3Client(s3)

	chConn, err := clickhouse.Open(ctx, &cfg.ClickHouse)
	if err != nil {
		log.Fatal("failed to init clickhouse: ", err)
	}
	if chConn == nil {
		logger.Warn("clickhouse not configured: message archive export and history are disabled")
	} else {
		defer chConn.Close()
	}
	historyReader := history.NewReader(db, chConn)

	connManager := conns.NewConnectionManager(ctx, logger.WithGroup("conns"), nil)

	procService := processor.NewService(logger.WithGroup("service"), db, s3, ffmpegClient, ttsEngine, chatTTSEngine, whisper, imageLlm, textFilter, connManager)

	aiHandler := processor.NewAIHandler(logger.WithGroup("ai_handler"), characterLlm, imageLlm, cfg.NativeImages, db, s3, procService)
	ttsHandler := processor.NewTTSHandler(logger.WithGroup("tts_handler"), db, procService)
	universalHandler := processor.NewUniversalHandler(logger.WithGroup("universal_handler"), db, procService)

	agenticDetector := agentic.NewDetector(oaiClient)
	agenticPlanner := agentic.NewPlanner(oaiClient)
	agenticHandler := processor.NewAgenticHandler(logger.WithGroup("agentic_handler"), db, agenticDetector, agenticPlanner, characterLlm, procService)
	chatTTSHandler := processor.NewChatTTSHandler(logger.WithGroup("chat_tts_handler"), db, procService)

	proc := processor.NewProcessor(logger.WithGroup("processor"), db, connManager, aiHandler, ttsHandler, universalHandler, agenticHandler, chatTTSHandler)

	conns.SetProcessor(connManager, proc)

	twitchClient := twitch.New(httpClient, &cfg.Twitch)

	api := api.NewAPI(&cfg.Api, cfg.Ingest.Host, cfg.Ingest.Port, &cfg.EmoteService, logger.WithGroup("api"), connManager, twitchClient, db, s3, ffmpegClient, ttsHandler, aiHandler, universalHandler, agenticHandler, procService, historyReader)

	router := api.NewRouter()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

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
				if err := db.CleanQueue(ctx, chConn != nil); err != nil {
					logger.Error("failed to clean db msg queue", "err", err)
				}
			case <-ctx.Done():
				ticker.Stop()
				break loop
			}
		}
	}()

	if chConn != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			history.NewExporter(logger.WithGroup("archive_export"), db, chConn).Run(ctx)
		}()
	}

	wg.Add(1)
	go func() {
		defer wg.Done()

		monitoring.MonitoringLoop(ctx, logger.WithGroup("nvidia"))
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

	logins := make([]string, 0, len(users))
	for _, user := range users {
		logins = append(logins, user.TwitchLogin)
	}
	logger.Info("got users from db", "users", logins)

	for _, user := range users {
		cm.HandleUser(user)
	}

	cm.Wait()

	return nil
}
