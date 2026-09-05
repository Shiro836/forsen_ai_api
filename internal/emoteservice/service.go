// Package emoteservice classifies 7TV emotes offline and answers per-channel
// render verdicts for them. Nothing here runs on the message path: the overlay
// only ever reads a cached verdict.
package emoteservice

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"app/pkg/infinity"
	"app/pkg/oai"
	"app/pkg/s3client"
	"app/pkg/seventv"

	"golang.org/x/sync/errgroup"
)

// ChannelSetSource resolves 7TV emote sets, by Twitch channel or by set id.
// These are plain metadata calls and never trigger classification on their own.
type ChannelSetSource interface {
	GetTwitchUser(ctx context.Context, twitchID string) (*seventv.TwitchUser, error)
	GetEmoteSet(ctx context.Context, setID string) (*seventv.EmoteSet, error)
}

type Service struct {
	logger   *slog.Logger
	cfg      *Config
	store    *Store
	worker   *Worker
	matcher  *RuleMatcher
	channels ChannelSetSource

	// runCtx is the service's own lifetime. A background job outlives the request
	// that started it and must end with the service rather than with the handler.
	runCtx     context.Context
	reflashing atomic.Bool
}

// jobContext is what a background job started from a handler runs under.
func (s *Service) jobContext() context.Context {
	if s.runCtx == nil {
		return context.Background()
	}
	return s.runCtx
}

func New(ctx context.Context, logger *slog.Logger, cfg *Config) (*Service, error) {
	cfg.withDefaults()

	if cfg.SharedSecret == "" {
		return nil, fmt.Errorf("emoteservice: shared_secret is required")
	}

	store, err := NewStore(ctx, cfg.ConnStr)
	if err != nil {
		return nil, err
	}
	if err := store.Migrate(ctx); err != nil {
		store.Close()
		return nil, err
	}

	vision := oai.New(&cfg.Vision)
	classifier := NewClassifier(logger.With("component", "classifier"), vision, cfg)
	sevenTV := seventv.New(&cfg.SevenTV)

	var embedder Embedder
	if cfg.Infinity.URL != "" {
		embedder = infinityEmbedder{client: infinity.New(&cfg.Infinity)}
	}

	var objects ObjectStore
	if cfg.S3.Endpoint != "" {
		s3, err := s3client.New(ctx, &cfg.S3)
		if err != nil {
			store.Close()
			return nil, fmt.Errorf("emoteservice: init s3: %w", err)
		}
		objects = s3
	} else {
		logger.Warn("s3 not configured, emote originals and grids will not be kept")
	}

	return &Service{
		logger:   logger,
		cfg:      cfg,
		store:    store,
		worker:   NewWorker(logger.With("component", "cache-builder"), store, sevenTV, sevenTV, classifier, embedder, objects, cfg),
		matcher:  NewRuleMatcher(embedder, store, cfg),
		channels: sevenTV,
	}, nil
}

func (s *Service) Close() { s.store.Close() }

// Run serves the API and drains the classification queue until ctx ends.
func (s *Service) Run(ctx context.Context) error {
	s.runCtx = ctx
	addr := fmt.Sprintf("%s:%d", s.cfg.Host, s.cfg.Port)
	srv := &http.Server{
		Addr:              addr,
		Handler:           s.Router(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	group, ctx := errgroup.WithContext(ctx)

	group.Go(func() error {
		s.logger.Info("starting emote service http server", "addr", addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("emote service http server: %w", err)
		}
		return nil
	})

	group.Go(func() error {
		if err := s.worker.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			return fmt.Errorf("emote cache builder: %w", err)
		}
		return nil
	})

	group.Go(func() error {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	})

	if err := group.Wait(); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}

type infinityEmbedder struct{ client *infinity.Client }

func (e infinityEmbedder) EmbedText(ctx context.Context, texts []string) ([][]float32, error) {
	return e.client.EmbedText(ctx, texts)
}

func (e infinityEmbedder) EmbedPNGs(ctx context.Context, images [][]byte) ([][]float32, error) {
	imgs := make([]infinity.Image, 0, len(images))
	for _, data := range images {
		imgs = append(imgs, infinity.Image{MIME: "image/png", Data: data})
	}
	return e.client.EmbedImages(ctx, imgs)
}
