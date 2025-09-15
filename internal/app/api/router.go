package api

import (
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"time"

	"app/db"
	"app/internal/app/conns"
	"app/internal/app/notifications"
	"app/pkg/ai"
	"app/pkg/llm"
	"app/pkg/s3client"
	"app/pkg/twitch"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	slogchi "github.com/samber/slog-chi"
)

type Config struct {
	Port    int           `yaml:"port"`
	Timeout time.Duration `yaml:"timeout"`
}

type API struct {
	logger *slog.Logger

	connManager *conns.Manager

	controlPanelNotifications *notifications.Client

	styleTts *ai.StyleTTSClient
	llm      *llm.Client

	twitchClient *twitch.Client

	db *db.DB

	s3 *s3client.Client

	cfg *Config

	workersLock sync.Mutex // lock because we don't want to have a situation when both "add permission" and "create user" are called at the same time, and user worker is not started
}

func NewAPI(cfg *Config, logger *slog.Logger, connManager *conns.Manager, controlPanelNotifications *notifications.Client,
	twitchClient *twitch.Client, styleTts *ai.StyleTTSClient, llm *llm.Client, db *db.DB, s3 *s3client.Client) *API {
	return &API{
		cfg: cfg,

		logger: logger,

		connManager: connManager,

		controlPanelNotifications: controlPanelNotifications,

		twitchClient: twitchClient,

		styleTts: styleTts,
		llm:      llm,

		db: db,

		s3: s3,
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

	router.Use(middleware.Recoverer)

	router.Use(api.AuthMiddleware)

	router.Get("/settings", http.RedirectHandler("/", http.StatusMovedPermanently).ServeHTTP)

	router.Get("/twitch_redirect_handler", http.HandlerFunc(api.twitchRedirectHandler))

	router.Post("/request_permissions/{permission}", http.HandlerFunc(api.requestPermissions))

	// Images upload page and retrieval (registered before catch-all route)
	router.Get("/images", api.navPublic(api.imagesPage))
	router.Post("/images", http.HandlerFunc(api.imagesUpload))
	router.Get("/images/{id}", http.HandlerFunc(api.imageGet))

	// START No perms routes

	router.Get("/{twitch_login}", api.elemNoPermissions(api.obsOverlay))
	router.Get("/ws/{twitch_login}", api.wsHandler) // permission is checked based on param cuz there is no auth cookie in obs

	router.Get("/characters/{character_id}/image", api.charImage)

	// END

	router.Get("/control", api.nav(api.controlPanelMenu))
	router.Get("/control/ws/{twitch_user_id}", api.controlPanelWSConn)
	router.Get("/control/{twitch_user_id}", api.nav(api.controlPanel))

	router.Group(func(router chi.Router) {
		router.Use(api.checkPermissions(db.PermissionStreamer))

		router.Get("/", api.nav(api.home))
		router.Get("/characters", api.nav(api.characters))

		router.Get("/characters/{character_id}", api.nav(api.character))
		router.Post("/characters/{character_id}", api.upsertCharacter)

		router.Get("/new_message_example/{id}", api.elem(api.newMessageExample))

		// router.Get("/characters/{character_id}/image", api.charImage)
		// router.Get("/characters/{character_id}/audio", api.charAudio)

		router.Post("/characters/{character_id}/reward_tts", api.reward(db.TwitchRewardTTS))
		router.Post("/characters/{character_id}/reward_ai", api.reward(db.TwitchRewardAI))

		router.Post("/control/grant", http.HandlerFunc(api.controlPanelGrant))
		router.Post("/control/revoke", http.HandlerFunc(api.controlPanelRevoke))

		router.Get("/filters", api.nav(api.filters))
		router.Post("/filters", api.updateFilters)
	})

	router.Group(func(router chi.Router) {
		router.Use(api.checkPermissions(db.PermissionAdmin))

		router.Get("/admin", api.nav(api.admin))

		router.Post("/admin/add_mod", http.HandlerFunc(api.managePermission(permissionActionAdd, db.PermissionMod)))
		router.Post("/admin/remove_mod", http.HandlerFunc(api.managePermission(permissionActionRemove, db.PermissionMod)))

		router.Post("/admin/add_admin", http.HandlerFunc(api.managePermission(permissionActionAdd, db.PermissionAdmin)))
		router.Post("/admin/remove_admin", http.HandlerFunc(api.managePermission(permissionActionRemove, db.PermissionAdmin)))

		router.Post("/admin/add_relation", http.HandlerFunc(api.manageRelation(db.RelationTypeModerating)))
	})

	router.Group(func(router chi.Router) {
		router.Use(api.checkPermissions(db.PermissionMod))

		router.Get("/mod", api.nav(api.mod))

		router.Post("/add_streamer", http.HandlerFunc(api.managePermission(permissionActionAdd, db.PermissionStreamer)))
		router.Post("/remove_streamer", http.HandlerFunc(api.managePermission(permissionActionRemove, db.PermissionStreamer)))

		router.Post("/restart", func(w http.ResponseWriter, r *http.Request) {
			cmds := [][]string{
				// {"sudo", "systemctl", "restart", "lexi"},
				// {"sudo", "systemctl", "restart", "style"},
				{"docker", "restart", "whisper-api"},
			}
			for _, cmdArgs := range cmds {
				cmd := exec.Command(cmdArgs[0], cmdArgs[1:]...)
				if err := cmd.Run(); err != nil {
					api.logger.Error("Failed to restart service", "error", err, "command", cmdArgs)
					w.Write([]byte("failed to restart service: " + err.Error()))

					return
				}
			}

			time.AfterFunc(300*time.Millisecond, func() {
				os.Exit(0)
			})

			w.WriteHeader(http.StatusOK)
			w.Write([]byte("restart signal sent"))
		})
	})

	router.Handle("/static/*", http.FileServerFS(staticFS))
	router.Handle("/favicon.ico", http.RedirectHandler("/static/logo.jpg", http.StatusMovedPermanently))

	router.Get("/empty", api.elem(empty))

	return router
}
