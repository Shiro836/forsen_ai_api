package api

import (
	"app/db"
	"app/pkg/ctxstore"
	"net/http"
)

const cookieSessionID = "session_id"

type htmlErr struct {
	ErrorCode    int
	ErrorMessage string
}

func (api *API) AuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(cookieSessionID)
		if err == nil {
			session := cookie.Value

			user, err := api.db.GetUserBySession(r.Context(), session)
			// reset cookie and redirect to login
			if err != nil {
				if db.ErrCode(err) == db.ErrCodeNoRows {
					http.SetCookie(w, &http.Cookie{
						Name:   cookieSessionID,
						Value:  "",
						MaxAge: -1,
					})

					http.Redirect(w, r, "/", http.StatusFound)

					return
				} else {
					w.WriteHeader(http.StatusInternalServerError)
					submitPage(w, errPage(r, http.StatusInternalServerError, err.Error()))

					return
				}
			}

			r = r.WithContext(ctxstore.WithUser(r.Context(), user))
		}

		next.ServeHTTP(w, r)
	})
}
