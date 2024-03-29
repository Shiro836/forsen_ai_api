package api

import (
	"app/conns"
	"app/db"
	"app/tts"
	"app/twitch"
	"log/slog"
	"net/http"
	"os"
	"slices"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type Config struct {
	Port int `yaml:"port"`
}

type API struct {
	ttsClient   *tts.Client
	connManager *conns.Manager
	twitch      *twitch.Client
}

func NewAPI(connManager *conns.Manager, twitch *twitch.Client, ttsClient *tts.Client) *API {
	return &API{
		connManager: connManager,
		twitch:      twitch,
		ttsClient:   ttsClient,
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

	router.Get("/", descriptionHandler)
	router.Get("/{user}", api.betaHtmlHandler)
	router.Get("/ws/{user}", api.consumerHandler)

	router.Get("/settings", settingsHandler)
	router.Get("/add_channel_point_reward/{reward_id}", api.channelPointsRewardCreateHandler)

	router.Get("/twitch_token_handler", api.twitchTokenHandler)

	router.Get("/get_settings", getSettings)
	router.Post("/update_settings", api.updateSettings)

	router.Get("/get_whitelist", getWhitelist)
	router.Post("/update_whitelist", api.updateWhitelist)

	router.Post("/upload_card/{char_name}", api.UploadCharCardHandler)
	router.Post("/upload_voice/{char_name}", api.UploadVoiceHandler)
	router.Post("/upload_model/{char_name}", api.UploadModel)

	router.Get("/get_model/{char_name}", api.GetModel)

	router.Post("/update_filters", api.UpdateFilters)
	router.Get("/get_filters", api.GetFilters)

	router.Post("/upload_full_card", api.UploadFullCardHandler)
	router.Get("/delete_full_card/{char_name}", api.DeleteFullCardHandler)
	router.Get("/get_full_card_list", api.GetFullCardListHandler)
	router.Get("/get_full_card/{char_name}", api.GetFullCardHandler)
	router.Get("/get_all_cards", api.GetAllCards)

	router.Post("/tts", api.TTS)

	router.Get("/get_queue/{state}/{updated}", api.GetQue)
	router.Delete("/delete_queue_msg/{msg_id}", api.DeleteMsgFromQue)

	router.Get("/favicon.ico", faviconHandler)

	fs := http.FileServer(http.Dir("client/static"))

	router.Handle("/static/*", http.StripPrefix("/static/", fs))

	router.Handle("/metrics", promhttp.Handler())

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
