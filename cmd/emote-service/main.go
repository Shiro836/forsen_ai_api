package main

import (
	"context"
	"flag"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"app/cfg"
	"app/internal/emoteservice"

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
		log.Fatal("can't unmarshal cfg.yaml file: ", err)
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service", "emote")
	slog.SetDefault(logger)

	initCtx, cancelInit := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelInit()

	svc, err := emoteservice.New(initCtx, logger, &config.EmoteService)
	if err != nil {
		log.Fatal("failed to create emote service: ", err)
	}
	defer svc.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-stop
		logger.Info("interrupt triggered")
		cancel()
	}()

	if err := svc.Run(ctx); err != nil {
		logger.Error("emote service stopped", "err", err)
		os.Exit(1)
	}
}
