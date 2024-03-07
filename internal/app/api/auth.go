package api

import (
	"app/db"
	"app/pkg/ctxstore"
	"html/template"
	"net/http"
	"slices"
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

func (api *API) checkPermissions(requiredPermissions ...db.Permission) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			user := ctxstore.GetUser(r.Context())

			if user == nil {
				w.WriteHeader(http.StatusForbidden)
				_ = html.ExecuteTemplate(w, "error.html", &htmlErr{
					ErrorCode:    http.StatusForbidden,
					ErrorMessage: "No user found, you are not supposed to be here. How did you get here??????",
				})

				return
			}

			userPermissions, err := api.db.GetUserPermissions(r.Context(), user.ID, db.StatusGranted)
			if err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				_ = html.ExecuteTemplate(w, "error.html", &htmlErr{
					ErrorCode:    http.StatusInternalServerError,
					ErrorMessage: err.Error(),
				})

				return
			}

			notFoundPermissions := make([]permission, 0, len(requiredPermissions))

			for _, requiredPermission := range requiredPermissions {
				if !slices.Contains(userPermissions, requiredPermission) {
					notFoundPermissions = append(notFoundPermissions, permission{
						PermissionID:   int(requiredPermission),
						PermissionName: requiredPermission.String(),
					})
				}
			}

			if len(notFoundPermissions) > 0 {
				w.WriteHeader(http.StatusForbidden)
				api.nav(func(r *http.Request) template.HTML {
					return getHtml("no_permissions.html", &noPermissionsPage{
						Permissions: notFoundPermissions,
					})
				}).ServeHTTP(w, r)

				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
