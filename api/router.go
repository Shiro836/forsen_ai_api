package api

import (
	"app/db"
	"app/internal/app/conns"
	"app/pkg/ai"
	"app/pkg/twitch"
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
)

type Config struct {
	Domain  string        `yaml:"domain"`
	Port    int           `yaml:"port"`
	Timeout time.Duration `yaml:"timeout"`
}

type DB interface {
	GetUserBySession(ctx context.Context, session string) (*db.User, error)
}

type API struct {
	logger *slog.Logger

	connManager *conns.Manager

	styleTts *ai.StyleTTSClient
	metaTts  *ai.MetaTTSClient
	llm      *ai.VLLMClient

	twitchClient *twitch.Client

	db DB

	cfg *Config
}

func NewAPI(cfg *Config, logger *slog.Logger, connManager *conns.Manager, twitchClient *twitch.Client, styleTts *ai.StyleTTSClient, metaTts *ai.MetaTTSClient, llm *ai.VLLMClient, db DB) *API {
	return &API{
		cfg: cfg,

		logger: logger,

		connManager: connManager,

		twitchClient: twitchClient,

		styleTts: styleTts,
		metaTts:  metaTts,
		llm:      llm,

		db: db,
	}
}

func (api *API) NewRouter() *chi.Mux {
	router := chi.NewRouter()

	router.Use(cors.Handler(cors.Options{
		AllowedOrigins:   []string{"*"},
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"*"},
		ExposedHeaders:   []string{"Link"},
		AllowCredentials: true,
		MaxAge:           300,
	}))
	router.Use(middleware.RequestID)
	router.Use(middleware.StripSlashes)
	router.Use(middleware.Recoverer)

	router.Use(api.AuthMiddleware)

	router.Get("/", http.HandlerFunc(index))
	router.Get("/settings", http.HandlerFunc(settings))

	router.Handle("/static/*", http.FileServer(http.FS(staticFS)))

	return router
}
