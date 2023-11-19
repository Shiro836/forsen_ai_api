package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

func webSocketHandler(w http.ResponseWriter, r *http.Request) {
	user := chi.URLParam(r, "user")
	if !isValidUser(user, w) {
		return
	}

	w.WriteHeader(http.StatusNotImplemented)

	w.Header().Add("Content-Type", "text/html")
	w.Write([]byte("not implemented"))
}
