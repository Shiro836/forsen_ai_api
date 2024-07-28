package api

import (
	"app/db"
	"app/pkg/ctxstore"
	"fmt"
	"html/template"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
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
						Name:  cookieSessionID,
						Value: "",

						Path:   "/",
						MaxAge: -1,
					})

					http.Redirect(w, r, "/", http.StatusFound)

					return
				} else {
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
				submitPage(w, authPage(r))

				return
			}

			userPermissions, err := api.db.GetUserPermissions(r.Context(), user.ID, db.PermissionStatusGranted)
			if err != nil {
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

func (api *API) twitchRedirectHandler(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	if len(code) == 0 {
		submitPage(w, errPage(r, http.StatusInternalServerError, r.URL.Query().Get("error_description")))

		return
	}

	user, err := api.twitchClient.CodeHandler(code)
	if err != nil {
		submitPage(w, errPage(r, http.StatusInternalServerError, err.Error()))

		return
	}

	user.Session = uuid.NewString()

	id, err := api.db.UpsertUser(r.Context(), user)
	if err != nil {
		submitPage(w, errPage(r, http.StatusInternalServerError, err.Error()))

		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:  cookieSessionID,
		Value: user.Session,

		Path:    "/",
		Expires: time.Now().Add(time.Hour * 24 * 365),

		Secure: true,
	})

	user.ID = id

	_ = api.handleNewUser(r.Context(), user)

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (api *API) requestPermissions(w http.ResponseWriter, r *http.Request) {
	permissionStr := chi.URLParam(r, "permission")
	permissionInt, err := strconv.Atoi(permissionStr)
	permission := db.Permission(permissionInt)

	if err != nil || !db.IsValidPermission(permission) {
		_ = html.ExecuteTemplate(w, "error.html", &htmlErr{
			ErrorCode:    http.StatusBadRequest,
			ErrorMessage: "Invalid permission provided",
		})

		return
	}

	user := ctxstore.GetUser(r.Context())
	if user == nil {
		_ = html.ExecuteTemplate(w, "error.html", &htmlErr{
			ErrorCode:    http.StatusForbidden,
			ErrorMessage: "No user found, you are not supposed to be here. How did you get here??????",
		})

		return
	}

	err = api.db.RequestAccess(r.Context(), user, permission)
	if err != nil {
		if db.ErrCode(err) == db.ErrCodeAlreadyExists {
			_, _ = w.Write([]byte("Already Requested"))

			return
		}

		_ = html.ExecuteTemplate(w, "error.html", &htmlErr{
			ErrorCode:    http.StatusInternalServerError,
			ErrorMessage: err.Error(),
		})

		return
	}

	_, _ = w.Write([]byte("Requested"))
}

func (api *API) admin(r *http.Request) template.HTML {
	return getHtml("admin.html", nil)
}

func (api *API) mod(r *http.Request) template.HTML {
	requestUsers, err := api.db.GetUsersPermissions(r.Context(), db.PermissionStreamer, db.PermissionStatusWaiting)
	if err != nil {
		return getHtml("error.html", &htmlErr{
			ErrorCode:    http.StatusInternalServerError,
			ErrorMessage: fmt.Sprintf("db get users permissions err: %v", err),
		})
	}

	requests := make([]permissionRequest, 0, len(requestUsers))
	for _, user := range requestUsers {
		requests = append(requests, permissionRequest{
			Login:  user.TwitchLogin,
			UserID: user.ID,
		})
	}

	approvedUsers, err := api.db.GetUsersPermissions(r.Context(), db.PermissionStreamer, db.PermissionStatusGranted)
	if err != nil {
		return getHtml("error.html", &htmlErr{
			ErrorCode:    http.StatusInternalServerError,
			ErrorMessage: fmt.Sprintf("db get users permissions err: %v", err),
		})
	}

	approved := make([]permissionRequest, 0, len(approvedUsers))
	for _, user := range approvedUsers {
		approved = append(approved, permissionRequest{
			Login:  user.TwitchLogin,
			UserID: user.ID,
		})
	}

	return getHtml("mod.html", &modPage{
		Requests:  requests,
		Streamers: approved,
	})
}
