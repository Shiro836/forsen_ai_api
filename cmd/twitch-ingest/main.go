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
	"app/internal/app/ingest"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
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

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	createDbCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	database, err := db.New(createDbCtx, &cfg.DB)
	if err != nil {
		log.Fatal("failed to init postgre db: ", err)
	}
	defer database.Close()

	svc := ingest.NewService(logger.WithGroup("ingest"), database, &cfg.Twitch)

	reg := prometheus.NewRegistry()
	ingest.RegisterMetrics(reg)

	if cfg.Ingest.Port == 0 {
		log.Fatal("ingest port must be set in config")
	}

	metricsAddr := fmt.Sprintf("%s:%d", cfg.Ingest.Host, cfg.Ingest.Port)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := svc.Run(ctx); err != nil {
			logger.Error("ingest service failed", "err", err)
			stop <- os.Interrupt
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()

		mux := http.NewServeMux()
		mux.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{Registry: reg}))
		mux.HandleFunc("/restart", func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}

			logger.Info("restart requested via ingest /restart endpoint")
			w.WriteHeader(http.StatusAccepted)
			w.Write([]byte("ingest restart scheduled"))

			go func() {
				time.Sleep(200 * time.Millisecond)
				cancel()
			}()
		})

		logger.Info("starting metrics server", "addr", metricsAddr)
		srv := &http.Server{
			Addr:    metricsAddr,
			Handler: mux,
		}

		go func() {
			<-ctx.Done()
			srv.Shutdown(context.Background())
		}()

		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("metrics server failed", "err", err)
		}
	}()

	select {
	case <-ctx.Done():
	case <-stop:
		logger.Info("Interrupt triggerred")
		cancel()
	}

	wg.Wait()
}
