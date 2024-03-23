package api

import (
	"app/pkg/ctxstore"
	"html/template"
	"net/http"
)

func (api *API) home(r *http.Request) template.HTML {
	user := ctxstore.GetUser(r.Context())
	if user == nil {
		return getHtml("error.html", &htmlErr{
			ErrorCode:    http.StatusInternalServerError,
			ErrorMessage: "no user found, very unlucky",
		})
	}

	return getHtml("home.html", &homePage{
		URL: r.Host + "/" + user.TwitchLogin,
	})
}

func (api *API) filters(r *http.Request) template.HTML {
	return getHtml("filters.html", nil)
}
