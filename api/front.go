package api

import (
	"app/db"
	"fmt"
	"net/http"
	"os"

	"github.com/dchest/uniuri"
	"github.com/go-chi/chi/v5"
)

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

func (api *API) betaHtmlHandler(w http.ResponseWriter, r *http.Request) {
	user := chi.URLParam(r, "user")
	if !isValidUser(user, w) {
		return
	}

	data, err := os.ReadFile("client/v2.html")
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("failed to read v2.html"))

		return
	}

	w.Header().Add("Content-Type", "text/html")
	w.Write(data)
}

func (api *API) betaJsHandler(w http.ResponseWriter, r *http.Request) {
	data, err := os.ReadFile("client/v2.js")
	if err != nil {
		w.WriteHeader(500)
		w.Write([]byte("failed to read v2.js"))
	}

	w.Header().Add("Content-Type", "application/javascript")
	w.Write(data)
}

func onUserData(userData *db.UserData) error {
	session := uniuri.New()

	userData.Session = session
	if err := db.UpsertUserData(userData); err != nil {
		return fmt.Errorf("failed to upsert user data: %w", err)
	}

	return nil
}

func (api *API) twitchTokenHandler(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	if len(code) == 0 {
		fmt.Println(r.URL.Query().Get("error"), r.URL.Query().Get("error_description"))
	} else if userData, err := api.twitch.CodeHandler(code); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(err.Error()))
	} else if err := onUserData(userData); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(err.Error()))
	} else {
		http.SetCookie(w, &http.Cookie{Name: "session_id", Value: userData.Session})

		http.Redirect(w, r, "https://forsen.fun/settings", http.StatusSeeOther)
	}
}
