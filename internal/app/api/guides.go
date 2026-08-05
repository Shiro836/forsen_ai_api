package api

import (
	"app/pkg/ctxstore"
	"html/template"
	"net/http"
)

type guideStep struct {
	Number       int
	Title        string
	PreviousPath string
	NextPath     string
}

type guidePage struct {
	Step guideStep
	URL  string
}

var obsGuideSteps = map[string]guideStep{
	"/": {
		Number:   1,
		Title:    "Add a Browser Source",
		NextPath: "/guide/browser-source",
	},
	"/guide/browser-source": {
		Number:       2,
		Title:        "Choose Browser Source",
		PreviousPath: "/",
		NextPath:     "/guide/source-name",
	},
	"/guide/source-name": {
		Number:       3,
		Title:        "Create Source",
		PreviousPath: "/guide/browser-source",
		NextPath:     "/guide/browser-properties",
	},
	"/guide/browser-properties": {
		Number:       4,
		Title:        "Configure the Browser Source",
		PreviousPath: "/guide/source-name",
		NextPath:     "/guide/verify",
	},
	"/guide/verify": {
		Number:       5,
		Title:        "Verify the Source",
		PreviousPath: "/guide/browser-properties",
	},
}

var audioGuideSteps = map[string]guideStep{
	"/guide/audio": {
		Number:   1,
		Title:    "Install the Audio Monitor Plugin",
		NextPath: "/guide/audio/filters",
	},
	"/guide/audio/filters": {
		Number:       2,
		Title:        "Open Source Filters",
		PreviousPath: "/guide/audio",
		NextPath:     "/guide/audio/add-filter",
	},
	"/guide/audio/add-filter": {
		Number:       3,
		Title:        "Add an Audio Filter",
		PreviousPath: "/guide/audio/filters",
		NextPath:     "/guide/audio/choose-monitor",
	},
	"/guide/audio/choose-monitor": {
		Number:       4,
		Title:        "Choose Audio Monitor",
		PreviousPath: "/guide/audio/add-filter",
		NextPath:     "/guide/audio/filter-name",
	},
	"/guide/audio/filter-name": {
		Number:       5,
		Title:        "Name the Filter",
		PreviousPath: "/guide/audio/choose-monitor",
		NextPath:     "/guide/audio/settings",
	},
	"/guide/audio/settings": {
		Number:       6,
		Title:        "Configure the Monitor",
		PreviousPath: "/guide/audio/filter-name",
	},
}

func (api *API) obsGuide(r *http.Request) template.HTML {
	user := ctxstore.GetUser(r.Context())
	if user == nil {
		return getHtml("error.html", &htmlErr{
			ErrorCode:    http.StatusInternalServerError,
			ErrorMessage: "no user found, very unlucky",
		})
	}

	step, ok := obsGuideSteps[r.URL.Path]
	if !ok {
		return getHtml("error.html", &htmlErr{
			ErrorCode:    http.StatusNotFound,
			ErrorMessage: "guide step not found",
		})
	}

	return getHtml("obs_guide.html", &guidePage{
		Step: step,
		URL:  r.Host + "/" + user.TwitchLogin,
	})
}

func (api *API) audioGuide(r *http.Request) template.HTML {
	step, ok := audioGuideSteps[r.URL.Path]
	if !ok {
		return getHtml("error.html", &htmlErr{
			ErrorCode:    http.StatusNotFound,
			ErrorMessage: "guide step not found",
		})
	}

	return getHtml("audio_guide.html", &guidePage{Step: step})
}

func (api *API) legacyHome(r *http.Request) template.HTML {
	user := ctxstore.GetUser(r.Context())
	if user == nil {
		return getHtml("error.html", &htmlErr{
			ErrorCode:    http.StatusInternalServerError,
			ErrorMessage: "no user found, very unlucky",
		})
	}

	return getHtml("legacy_home.html", &guidePage{URL: r.Host + "/" + user.TwitchLogin})
}
