package api

import (
	"app/db"
	"app/pkg/ctxstore"
	"html/template"
	"net/http"
	"strconv"
)

type filters struct {
	Filters     string
	TtsLimit    int
	MaxSfxCount int
}

func (api *API) filters(r *http.Request) template.HTML {
	user := ctxstore.GetUser(r.Context())
	if user == nil {
		return getHtml("error.html", &htmlErr{
			ErrorCode:    http.StatusInternalServerError,
			ErrorMessage: "no user found, very unlucky",
		})
	}

	settings, err := api.db.GetUserSettings(r.Context(), user.ID)
	if err != nil {
		return getHtml("error.html", &htmlErr{
			ErrorCode:    http.StatusInternalServerError,
			ErrorMessage: "failed to get user settings: " + err.Error(),
		})
	}

	ttsLimit := db.DefaultTtsLimitSeconds
	if settings.TtsLimit != nil && *settings.TtsLimit > 0 {
		ttsLimit = *settings.TtsLimit
	}

	maxSfxCount := db.DefaultMaxSfxCount
	if settings.MaxSfxCount != nil {
		maxSfxCount = *settings.MaxSfxCount
	}

	return getHtml("filters.html", &filters{
		Filters:     settings.Filters,
		TtsLimit:    ttsLimit,
		MaxSfxCount: maxSfxCount,
	})
}

func (api *API) updateFilters(w http.ResponseWriter, r *http.Request) {
	user := ctxstore.GetUser(r.Context())
	if user == nil {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("unauthorized"))

		return
	}

	err := r.ParseForm()
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("failed to parse form: " + err.Error()))

		return
	}

	settings, err := api.db.GetUserSettings(r.Context(), user.ID)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("failed to get user settings: " + err.Error()))

		return
	}

	settings.Filters = r.Form.Get("filters")

	ttsLimitStr := r.Form.Get("tts_limit")
	if ttsLimitStr != "" {
		ttsLimit, err := strconv.Atoi(ttsLimitStr)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte("invalid tts_limit value: " + err.Error()))
			return
		}
		settings.TtsLimit = &ttsLimit
	}

	maxSfxCountStr := r.Form.Get("max_sfx_count")
	if maxSfxCountStr != "" {
		maxSfxCount, err := strconv.Atoi(maxSfxCountStr)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte("invalid max_sfx_count value: " + err.Error()))
			return
		}
		settings.MaxSfxCount = &maxSfxCount
	}

	err = api.db.UpdateUserData(r.Context(), user.ID, settings)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("failed to update user settings: " + err.Error()))

		return
	}

	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("success"))
}
