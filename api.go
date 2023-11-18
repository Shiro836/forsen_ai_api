package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"

	"app/conns"
	"app/db"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/nicklaw5/helix/v2"
)

type API struct {
	connectionManager *conns.Manager
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

	router.Get("/webrtc.js", jsFileHandler)
	router.Get("/whitelist.js", jsWhitelistHandler)

	router.Get("/", descriptionHandler)
	router.Get("/{user}", homeHandler)
	router.Get("/ws/{user}", webSocketHandler)

	router.Get("/settings", settingsHandler)
	router.Get("/add_channel_point_reward", channelPointsRewardCreateHandler)

	router.Get("/twitch_token_handler", twitchTokenHandler)

	router.Get("/get_settings", getSettings)
	router.Post("/update_settings", updateSettings)

	router.Get("/get_whitelist", getWhitelist)
	router.Post("/update_whitelist", updateWhitelist)

	router.Get("/v2.js", api.betaJsHandler)
	router.Get("/v2/{user}", api.betaHtmlHandler)
	router.Get("/ws/v2/{user}", api.consumerHandler)

	return router
}

func jsFileHandler(w http.ResponseWriter, r *http.Request) {
	data, err := os.ReadFile("client/webrtc.js")
	if err != nil {
		w.WriteHeader(500)
		w.Write([]byte("failed to read webrtc.js"))
	}

	w.Header().Add("Content-Type", "application/javascript")
	w.Write(data)
}

func jsWhitelistHandler(w http.ResponseWriter, r *http.Request) {
	data, err := os.ReadFile("client/whitelist.js")
	if err != nil {
		w.WriteHeader(500)
		w.Write([]byte("failed to read whitelist.js"))
	}

	w.Header().Add("Content-Type", "application/javascript")
	w.Write(data)
}

func descriptionHandler(w http.ResponseWriter, r *http.Request) {
	data, err := os.ReadFile("client/description.html")
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("failed to read description.html"))

		return
	}

	w.Header().Add("Content-Type", "text/html")
	w.Write(data)
}

func homeHandler(w http.ResponseWriter, r *http.Request) {
	user := chi.URLParam(r, "user")
	if !isValidUser(user, w) {
		return
	}

	data, err := os.ReadFile("client/index.html")
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("failed to read index.html"))

		return
	}

	w.Header().Add("Content-Type", "text/html")
	w.Write(data)
}

func settingsHandler(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie("session_id"); err == nil && len(cookie.Value) != 0 {
		data, err := os.ReadFile("client/settings.html")
		if err != nil {
			w.WriteHeader(500)
			w.Write([]byte("failed to read settings.html"))
		}

		w.Header().Add("Content-Type", "text/html")
		w.Write(data)

		return
	}

	data, err := os.ReadFile("client/settings_login.html")
	if err != nil {
		w.WriteHeader(500)
		w.Write([]byte("failed to read settings.html"))

		return
	}

	w.Header().Add("Content-Type", "text/html")
	w.Write(data)
}

func channelPointsRewardCreateHandler(w http.ResponseWriter, r *http.Request) {
	fmt.Println("add")

	if cookie, err := r.Cookie("session_id"); err != nil || len(cookie.Value) == 0 {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte("unauthorized"))
	} else if userData, err := db.GetUserDataBySessionId(cookie.Value); err != nil {
		fmt.Println(err)

		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal server error"))
	} else if !isValidUser(userData.UserLoginData.UserName, w) {
		return
	} else if twitchClient, err := helix.NewClientWithContext(r.Context(), &helix.Options{
		ClientID:        twitchClientID,
		ClientSecret:    twitchSecret,
		UserAccessToken: userData.AccessToken,
		RefreshToken:    userData.RefreshToken,
	}); err != nil {
		fmt.Println(err)

		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal server error"))
	} else if resp, err := twitchClient.CreateCustomReward(&helix.ChannelCustomRewardsParams{
		BroadcasterID: strconv.Itoa(userData.UserLoginData.UserId),

		Title:               "Forsen AI",
		Cost:                1,
		Prompt:              "No !ask needed. Forsen will react to this message",
		IsEnabled:           true,
		BackgroundColor:     "#00FF00",
		IsUserInputRequired: true,
	}); err != nil {
		fmt.Println(err)

		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal server error"))
	} else if len(resp.Data.ChannelCustomRewards) == 0 {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("No new rewards created"))
	} else if err = db.SaveRewardID(resp.Data.ChannelCustomRewards[0].ID, userData.Session); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal server error"))
	} else {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("success"))
	}
}

func getSettings(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie("session_id"); err != nil || len(cookie.Value) == 0 {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte("unauthorized"))
	} else if userData, err := db.GetUserDataBySessionId(cookie.Value); err != nil {
		fmt.Println(err)

		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("failed to get settings"))
	} else if settings, err := db.GetDbSettings(userData.UserLoginData.UserName); err != nil {
		fmt.Println(err)

		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("failed to get settings"))
	} else if data, err := json.Marshal(settings); err != nil {
		fmt.Println(err)

		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("failed to marshal settings"))
	} else {
		w.Write(data)
	}
}

func updateSettings(w http.ResponseWriter, r *http.Request) {
	var settings *db.Settings

	if cookie, err := r.Cookie("session_id"); err != nil || len(cookie.Value) == 0 {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte("unauthorized"))
	} else if data, err := io.ReadAll(r.Body); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("failed to read request body"))
	} else if err = json.Unmarshal(data, &settings); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("failed to unmarshal request json body"))
	} else if err = db.UpdateDbSettings(settings, cookie.Value); err != nil {
		fmt.Println(err)
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("failed to update settings in db"))
	} else {
		w.Write([]byte("success"))
	}
}
