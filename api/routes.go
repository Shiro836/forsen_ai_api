package api

import (
	"app/pkg/ctxstore"
	"net/http"
	"time"

	"github.com/google/uuid"
)

func redirectToIndex(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/", http.StatusMovedPermanently)
}

func index(w http.ResponseWriter, r *http.Request) {
	_, ok := ctxstore.GetUser(r.Context())
	page := createPage(r)
	page.Title = "BAJ AI"

	if !ok {
		page.Content = getHtml("index.html", &LoginPage{
			RedirectUrl: "https://id.twitch.tv/oauth2/authorize?response_type=code&client_id=zi6vy3y3iq38svpmlub5fd26uwsee8&redirect_uri=https://" + r.Host + "/twitch_redirect_handler&scope=channel:read:subscriptions+channel:manage:redemptions+moderator:read:followers",
		})

		submitPage(w, page)

		return
	}

	page.Content = getHtml("main.html", nil)
	submitPage(w, page)
}

func (api *API) twitchRedirectHandler(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	if len(code) == 0 {
		w.WriteHeader(http.StatusInternalServerError)
		submitPage(w, errPage(r, http.StatusInternalServerError, r.URL.Query().Get("error_description")))

		return
	}

	twitchUser, err := api.twitchClient.CodeHandler(code)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		submitPage(w, errPage(r, http.StatusInternalServerError, err.Error()))

		return
	}

	twitchUser.Session = uuid.NewString()

	_, err = api.db.UpsertUser(r.Context(), twitchUser)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		submitPage(w, errPage(r, http.StatusInternalServerError, err.Error()))

		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:  cookieSessionID,
		Value: twitchUser.Session,

		Path:    "/",
		Expires: time.Now().Add(time.Hour * 24 * 365),

		Secure: true,
	})

	http.Redirect(w, r, "/", http.StatusSeeOther)
}
