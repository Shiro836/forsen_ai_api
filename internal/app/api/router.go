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
	slogchi "github.com/samber/slog-chi"
)

type Config struct {
	Domain  string        `yaml:"domain"`
	Port    int           `yaml:"port"`
	Timeout time.Duration `yaml:"timeout"`
}

type DB interface {
	GetUserByID(ctx context.Context, userID int) (*db.User, error)
	GetUserBySession(ctx context.Context, session string) (*db.User, error)
	UpsertUser(ctx context.Context, user *db.User) (int, error)

	GetUsersPermissions(ctx context.Context, permission db.Permission, permissionStatus db.Status) ([]*db.User, error)
	GetUserPermissions(ctx context.Context, userID int, permissionStatus db.Status) ([]db.Permission, error)
	RequestAccess(ctx context.Context, user *db.User, permission db.Permission) error
	AddPermission(ctx context.Context, initiator *db.User, targetTwitchUserID int, targetTwitchLogin string, permission db.Permission) error
	RemovePermission(ctx context.Context, initiator *db.User, targetTwitchUserID int, permission db.Permission) error
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

	router.Use(middleware.RequestID)
	router.Use(slogchi.New(api.logger))

	router.Use(cors.Handler(cors.Options{
		AllowedOrigins:   []string{"*"},
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"*"},
		ExposedHeaders:   []string{"Link"},
		AllowCredentials: true,
		MaxAge:           300,
	}))
	router.Use(middleware.StripSlashes)

	// TODO: uncomment after debugging
	router.Use(middleware.Recoverer)

	router.Use(api.AuthMiddleware)

	router.Get("/settings", http.RedirectHandler("/", http.StatusMovedPermanently).ServeHTTP)

	router.Get("/twitch_redirect_handler", http.HandlerFunc(api.twitchRedirectHandler))

	router.Post("/request_permissions/{permission}", http.HandlerFunc(api.requestPermissions))

	router.Group(func(router chi.Router) {
		router.Use(api.checkPermissions(db.PermissionStreamer))

		router.Get("/", api.nav(api.home))
		router.Get("/characters", api.nav(api.characters))
		router.Get("/filters", api.nav(api.filters))
	})

	router.Group(func(router chi.Router) {
		router.Use(api.checkPermissions(db.PermissionAdmin))

		router.Get("/admin", api.nav(api.admin))

		router.Post("/add_mod", http.HandlerFunc(api.managePermission(permissionActionAdd, db.PermissionMod)))
		router.Post("/remove_mod", http.HandlerFunc(api.managePermission(permissionActionRemove, db.PermissionMod)))

		router.Post("/add_admin", http.HandlerFunc(api.managePermission(permissionActionAdd, db.PermissionAdmin)))
		router.Post("/remove_admin", http.HandlerFunc(api.managePermission(permissionActionRemove, db.PermissionAdmin)))
	})

	router.Group(func(router chi.Router) {
		router.Use(api.checkPermissions(db.PermissionMod))

		router.Get("/mod", api.nav(api.mod))

		router.Post("/add_streamer", http.HandlerFunc(api.managePermission(permissionActionAdd, db.PermissionStreamer)))
		router.Post("/remove_streamer", http.HandlerFunc(api.managePermission(permissionActionRemove, db.PermissionStreamer)))
	})

	router.Handle("/static/*", http.FileServer(http.FS(staticFS)))
	router.Handle("/favicon.ico", http.RedirectHandler("/static/logo.png", http.StatusMovedPermanently))

	return router
}
