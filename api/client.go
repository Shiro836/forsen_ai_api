package api

import (
	"app/conns"
	"app/db"
	"app/twitch"
	"log/slog"
	"net/http"
	"os"
	"slices"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
)

type Config struct {
	Port int `yaml:"port"`
}

type API struct {
	connManager *conns.Manager
	twitch      *twitch.Client
}

func NewAPI(connManager *conns.Manager, twitch *twitch.Client) *API {
	return &API{
		connManager: connManager,
		twitch:      twitch,
	}
}

func NewRouter(api *API) *chi.Mux {
	router := chi.NewRouter()

	router.Use(cors.Handler(cors.Options{
		AllowedOrigins:   []string{"*"},
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"*"},
		ExposedHeaders:   []string{"Link"},
		AllowCredentials: true,
		MaxAge:           300,
	}))
	router.Use(middleware.RealIP)
	router.Use(middleware.StripSlashes)
	router.Use(middleware.Recoverer)
	// TODO: add user authentication

	router.Get("/v2.js", api.betaJsHandler)
	router.Get("/whitelist.js", jsWhitelistHandler)

	router.Get("/", descriptionHandler)
	router.Get("/{user}", api.betaHtmlHandler)
	router.Get("/ws/{user}", api.consumerHandler)

	router.Get("/settings", settingsHandler)
	router.Get("/add_channel_point_reward", api.channelPointsRewardCreateHandler)

	router.Get("/twitch_token_handler", api.twitchTokenHandler)

	router.Get("/get_settings", getSettings)
	router.Post("/update_settings", updateSettings)

	router.Get("/get_whitelist", getWhitelist)
	router.Post("/update_whitelist", updateWhitelist)

	return router
}

func isValidUser(user string, w http.ResponseWriter) bool {
	if whitelist, err := db.GetDbWhitelist(); err != nil {
		slog.Error("failed to get whitelist", "err", err, "user", user)
		return false
	} else if !slices.ContainsFunc(whitelist.List, func(h *db.Human) bool {
		return strings.EqualFold(h.Login, user) && h.BannedBy == nil
	}) {
		w.WriteHeader(http.StatusForbidden)
		data, _ := os.ReadFile("client/whitelist.html")
		w.Write(data)

		slog.Info("whitelist rejected", "user", user)

		return false
	} else {
		return true
	}
}