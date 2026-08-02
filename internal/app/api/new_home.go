package api

import (
	"app/pkg/ctxstore"
	"html/template"
	"net/http"
)

type newHomeStep struct {
	Number       int
	Title        string
	PreviousPath string
	NextPath     string
}

type newHomePage struct {
	Step newHomeStep
	URL  string
}

var newHomeSteps = map[string]newHomeStep{
	"/new_home": {
		Number:   1,
		Title:    "Add a Browser Source",
		NextPath: "/new_home/browser-source",
	},
	"/new_home/browser-source": {
		Number:       2,
		Title:        "Choose Browser Source",
		PreviousPath: "/new_home",
		NextPath:     "/new_home/source-name",
	},
	"/new_home/source-name": {
		Number:       3,
		Title:        "Create Source",
		PreviousPath: "/new_home/browser-source",
		NextPath:     "/new_home/browser-properties",
	},
	"/new_home/browser-properties": {
		Number:       4,
		Title:        "Configure the Browser Source",
		PreviousPath: "/new_home/source-name",
		NextPath:     "/new_home/verify",
	},
	"/new_home/verify": {
		Number:       5,
		Title:        "Verify the Source",
		PreviousPath: "/new_home/browser-properties",
	},
}

func (api *API) newHome(r *http.Request) template.HTML {
	user := ctxstore.GetUser(r.Context())
	if user == nil {
		return getHtml("error.html", &htmlErr{
			ErrorCode:    http.StatusInternalServerError,
			ErrorMessage: "no user found, very unlucky",
		})
	}

	step, ok := newHomeSteps[r.URL.Path]
	if !ok {
		return getHtml("error.html", &htmlErr{
			ErrorCode:    http.StatusNotFound,
			ErrorMessage: "guide step not found",
		})
	}

	return getHtml("new_home.html", &newHomePage{
		Step: step,
		URL:  r.Host + "/" + user.TwitchLogin,
	})
}
