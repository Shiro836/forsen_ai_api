package api

import (
	"app/pkg/ctxstore"
	"database/sql"
	"errors"
	"net/http"

	"github.com/jritsema/gotoolbox/web"
)

const sessionID = "session_id"

type htmlErr struct {
	ErrorCode    int
	ErrorMessage string
}

func (api *API) AuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(sessionID)
		if err == nil {
			session := cookie.Value

			user, err := api.db.GetUserBySession(r.Context(), session)
			// reset cookie and redirect to login
			if err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					http.SetCookie(w, &http.Cookie{
						Name:   sessionID,
						Value:  "",
						MaxAge: -1,
					})

					http.Redirect(w, r, "/", http.StatusFound)

					return
				} else {
					web.HTML(http.StatusInternalServerError, html, "error.html", &htmlErr{
						ErrorCode:    http.StatusInternalServerError,
						ErrorMessage: err.Error(),
					}, nil).Write(w)

					return
				}
			}

			r = r.WithContext(ctxstore.WithUser(r.Context(), user))
		}

		next.ServeHTTP(w, r)
	})
}
