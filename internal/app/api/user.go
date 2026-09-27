package api

import (
	"app/db"
	"app/pkg/ctxstore"
	"html/template"
	"net/http"
	"strconv"
	"strings"
)

type filters struct {
	Filters                   string
	CustomFilterPrompt        string
	TtsLimit                  int
	MaxSfxCount               int
	SfxTotalLimit             int
	MaxSingCount              int
	Token                     string
	CloseRedemptions          bool
	DisableAudioNormalization bool
	DisableLLMFilter          bool
	DisableRegexFilter        bool
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

	sfxTotalLimit := db.DefaultSfxTotalLimit
	if settings.SfxTotalLimit != nil {
		sfxTotalLimit = *settings.SfxTotalLimit
	}

	maxSingCount := db.DefaultMaxSingCount
	if settings.MaxSingCount != nil {
		maxSingCount = *settings.MaxSingCount
	}

	return getHtml("filters.html", &filters{
		Filters:                   settings.Filters,
		CustomFilterPrompt:        settings.CustomFilterPrompt,
		TtsLimit:                  ttsLimit,
		MaxSfxCount:               maxSfxCount,
		SfxTotalLimit:             sfxTotalLimit,
		MaxSingCount:              maxSingCount,
		Token:                     settings.Token,
		CloseRedemptions:          settings.CloseRedemptions,
		DisableAudioNormalization: settings.DisableAudioNormalization,
		DisableLLMFilter:          settings.DisableLLMFilter,
		DisableRegexFilter:        settings.DisableRegexFilter,
	})
}

func (api *API) regenerateToken(w http.ResponseWriter, r *http.Request) {
	user := ctxstore.GetUser(r.Context())
	if user == nil {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("unauthorized"))
		return
	}

	token, err := api.db.GenerateUserToken(r.Context(), user.ID)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("failed to generate token"))
		return
	}

	// Return full token only in response body; UI will mask display
	w.Header().Set("Content-Type", "text/plain")
	_, _ = w.Write([]byte(token))
}

const settingsSaveStatusID = "settings_save_result"

func (api *API) updateFilters(w http.ResponseWriter, r *http.Request) {
	fail := func(code int, message string) {
		writeSaveError(w, r, settingsSaveStatusID, "", code, message)
	}

	user := ctxstore.GetUser(r.Context())
	if user == nil {
		fail(http.StatusUnauthorized, "unauthorized")
		return
	}

	err := r.ParseForm()
	if err != nil {
		fail(http.StatusBadRequest, "failed to parse form: "+err.Error())
		return
	}

	settings, err := api.db.GetUserSettings(r.Context(), user.ID)
	if err != nil {
		fail(http.StatusInternalServerError, "failed to get user settings: "+err.Error())
		return
	}

	settings.Filters = normalizeFilters(r.Form.Get("filters"))
	settings.CustomFilterPrompt = strings.TrimSpace(r.Form.Get("custom_filter_prompt"))
	settings.CloseRedemptions = r.Form.Get("close_redemptions") == "on"
	settings.DisableAudioNormalization = r.Form.Get("disable_audio_normalization") == "on"
	settings.DisableLLMFilter = r.Form.Get("disable_llm_filter") == "on"
	settings.DisableRegexFilter = r.Form.Get("disable_regex_filter") == "on"

	for _, limit := range []struct {
		field, label string
		dst          **int
	}{
		{"tts_limit", "TTS limit", &settings.TtsLimit},
		{"max_sfx_count", "max SFX count", &settings.MaxSfxCount},
		{"sfx_total_limit", "total SFX length", &settings.SfxTotalLimit},
		{"max_sing_count", "max sing count", &settings.MaxSingCount},
	} {
		raw := r.Form.Get(limit.field)
		if raw == "" {
			continue
		}
		value, err := strconv.Atoi(raw)
		if err != nil {
			fail(http.StatusBadRequest, limit.label+" must be a number")
			return
		}
		*limit.dst = &value
	}

	err = api.db.UpdateUserData(r.Context(), user.ID, settings)
	if err != nil {
		fail(http.StatusInternalServerError, "failed to update user settings: "+err.Error())
		return
	}

	writeSaved(w, r, settingsSaveStatusID, "")
}

func normalizeFilters(raw string) string {
	parts := strings.Split(raw, ",")
	clean := parts[:0]
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			clean = append(clean, p)
		}
	}
	return strings.Join(clean, ",")
}
