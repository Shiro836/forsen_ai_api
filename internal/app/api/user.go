package api

import (
	"app/pkg/ctxstore"
	"html/template"
	"net/http"
)

type filters struct {
	Filters string
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

	return getHtml("filters.html", &filters{
		Filters: settings.Filters,
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

	err = api.db.UpdateUserData(r.Context(), user.ID, settings)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("failed to update user settings: " + err.Error()))

		return
	}

	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("success"))
}
