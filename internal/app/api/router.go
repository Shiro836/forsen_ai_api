package api

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"sync"
	"time"

	"app/db"
	"app/internal/app/conns"
	"app/internal/app/processor"
	"app/pkg/s3client"
	"app/pkg/twitch"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	slogchi "github.com/samber/slog-chi"
)

type Config struct {
	Port    int           `yaml:"port"`
	Timeout time.Duration `yaml:"timeout"`
}

type API struct {
	logger *slog.Logger

	connManager *conns.Manager

	twitchClient *twitch.Client

	db *db.DB

	s3 *s3client.Client

	cfg *Config

	ttsHandler       processor.InteractionHandler
	aiHandler        processor.InteractionHandler
	universalHandler processor.InteractionHandler
	agenticHandler   processor.InteractionHandler

	workersLock sync.Mutex // lock because we don't want to have a situation when both "add permission" and "create user" are called at the same time, and user worker is not started

	ingestRestartURL string
}

func NewAPI(cfg *Config, ingestHost string, ingestPort int, logger *slog.Logger, connManager *conns.Manager,
	twitchClient *twitch.Client, db *db.DB, s3 *s3client.Client,
	ttsHandler processor.InteractionHandler, aiHandler processor.InteractionHandler, universalHandler processor.InteractionHandler, agenticHandler processor.InteractionHandler) *API {
	api := &API{
		cfg: cfg,

		logger: logger,

		connManager: connManager,

		twitchClient: twitchClient,

		db: db,

		s3: s3,

		ttsHandler:       ttsHandler,
		aiHandler:        aiHandler,
		universalHandler: universalHandler,
		agenticHandler:   agenticHandler,
	}

	if ingestPort > 0 {
		host := ingestHost
		if host == "" {
			host = "127.0.0.1"
		}

		api.ingestRestartURL = fmt.Sprintf("http://%s:%d/restart", host, ingestPort)
	}

	return api
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

	// Grafana reverse proxy
	grafanaURL, _ := url.Parse("http://localhost:2999")
	proxy := httputil.NewSingleHostReverseProxy(grafanaURL)
	router.HandleFunc("/grafana*", func(w http.ResponseWriter, r *http.Request) {
		proxy.ServeHTTP(w, r)
	})

	router.Handle("/metrics", promhttp.Handler())

	router.Group(func(router chi.Router) {
		router.Use(api.AuthMiddleware)

		router.Get("/settings", http.RedirectHandler("/", http.StatusMovedPermanently).ServeHTTP)

		router.Get("/twitch_redirect_handler", http.HandlerFunc(api.twitchRedirectHandler))

		router.Post("/request_permissions/{permission}", http.HandlerFunc(api.requestPermissions))

		// Images upload page and retrieval (registered before catch-all route)
		router.Get("/images", api.navPublic(api.imagesPage))
		router.Post("/images", http.HandlerFunc(api.imagesUpload))
		router.Get("/images/{id}", http.HandlerFunc(api.imageGet))

		// Public voices list (short names with images)
		router.Get("/voices", api.navPublic(api.voicesPublic))

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

			router.Get("/characters/{character_id}/try", api.nav(api.tryCharacter))
			router.Get("/ws/characters/{character_id}/try", api.tryCharacterWS)

			router.Get("/universal-tts/try", api.nav(api.tryUniversalTTS))
			router.Get("/ws/universal-tts/try", api.tryUniversalTTSWS)

			router.Get("/agentic/try", api.nav(api.tryAgentic))
			router.Get("/ws/agentic/try", api.tryAgenticWS)

			router.Get("/new_message_example/{id}", api.elem(api.newMessageExample))

			// router.Get("/characters/{character_id}/image", api.charImage)
			// router.Get("/characters/{character_id}/audio", api.charAudio)

			router.Post("/characters/{character_id}/reward_tts", api.reward(db.TwitchRewardTTS))
			router.Post("/characters/{character_id}/reward_ai", api.reward(db.TwitchRewardAI))

			router.Post("/universal-tts/reward", http.HandlerFunc(api.universalTTSReward))
			router.Post("/agentic/reward", http.HandlerFunc(api.agenticReward))

			router.Post("/control/grant", http.HandlerFunc(api.controlPanelGrant))
			router.Post("/control/revoke", http.HandlerFunc(api.controlPanelRevoke))

			router.Get("/filters", api.nav(api.filters))
			router.Post("/filters", api.updateFilters)
			router.Post("/token/regenerate", http.HandlerFunc(api.regenerateToken))
		})

		router.Group(func(router chi.Router) {
			router.Use(api.checkPermissions(db.PermissionAdmin))

			router.Get("/admin", api.nav(api.admin))

			router.Post("/admin/add_mod", http.HandlerFunc(api.managePermission(permissionActionAdd, db.PermissionMod)))
			router.Post("/admin/remove_mod", http.HandlerFunc(api.managePermission(permissionActionRemove, db.PermissionMod)))

			router.Post("/admin/add_admin", http.HandlerFunc(api.managePermission(permissionActionAdd, db.PermissionAdmin)))
			router.Post("/admin/remove_admin", http.HandlerFunc(api.managePermission(permissionActionRemove, db.PermissionAdmin)))

			router.Post("/admin/add_relation", http.HandlerFunc(api.manageRelation(permissionActionAdd, db.RelationTypeModerating)))
			router.Post("/admin/remove_relation", http.HandlerFunc(api.manageRelation(permissionActionRemove, db.RelationTypeModerating)))

			router.Post("/characters/{character_id}/admin/update_short_char_name", http.HandlerFunc(api.updateShortCharName))
		})

		router.Group(func(router chi.Router) {
			router.Use(api.checkPermissions(db.PermissionMod))

			router.Get("/mod", api.nav(api.mod))

			router.Post("/add_streamer", http.HandlerFunc(api.managePermission(permissionActionAdd, db.PermissionStreamer)))
			router.Post("/remove_streamer", http.HandlerFunc(api.managePermission(permissionActionRemove, db.PermissionStreamer)))

			router.Post("/restart", func(w http.ResponseWriter, r *http.Request) {
				if api.ingestRestartURL != "" {
					ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
					defer cancel()

					req, err := http.NewRequestWithContext(ctx, http.MethodPost, api.ingestRestartURL, nil)
					if err != nil {
						api.logger.Error("failed to build ingest restart request", "error", err)
						http.Error(w, "failed to restart ingest service", http.StatusInternalServerError)

						return
					}

					resp, err := http.DefaultClient.Do(req)
					if err != nil {
						api.logger.Error("failed to call ingest restart", "error", err)
						http.Error(w, "failed to restart ingest service", http.StatusBadGateway)

						return
					}
					defer resp.Body.Close()
					io.Copy(io.Discard, resp.Body)

					if resp.StatusCode >= http.StatusMultipleChoices {
						api.logger.Error("ingest restart returned non-success", "status", resp.StatusCode)
						http.Error(w, "ingest restart failed", http.StatusBadGateway)

						return
					}
				} else {
					api.logger.Warn("ingest restart url not configured; skipping ingest restart call")
				}

				cmds := [][]string{
					// {"sudo", "systemctl", "restart", "lexi"},
					// {"sudo", "systemctl", "restart", "style"},
					// {"docker", "restart", "whisper-api"},
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

		router.Get("/empty", api.elem(empty))
	})

	router.Handle("/static/*", http.FileServerFS(staticFS))
	router.Handle("/favicon.ico", http.RedirectHandler("/static/logo.jpg", http.StatusMovedPermanently))

	return router
}
