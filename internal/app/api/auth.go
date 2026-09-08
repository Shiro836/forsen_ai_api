package api

import (
	"app/db"
	"app/pkg/ctxstore"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
)

const (
	cookieSessionID  = "session_id"
	cookieOAuthState = "oauth_state"

	twitchClientID = "zi6vy3y3iq38svpmlub5fd26uwsee8"
	oauthStateTTL  = 10 * time.Minute
)

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
					http.SetCookie(w, sessionCookie("", -1))

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

// sessionCookie is host-only and Lax so it rides top-level navigations
// (the Twitch redirect back) but never cross-site POSTs or websocket
// handshakes; HttpOnly keeps it out of reach of injected script.
func sessionCookie(value string, maxAge int) *http.Cookie {
	return secureCookie(cookieSessionID, value, maxAge)
}

func stateCookie(value string, maxAge int) *http.Cookie {
	return secureCookie(cookieOAuthState, value, maxAge)
}

func secureCookie(name, value string, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		MaxAge:   maxAge,
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	}
}

func (api *API) login(w http.ResponseWriter, r *http.Request) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		submitPage(w, errPage(r, http.StatusInternalServerError, err.Error()))

		return
	}
	state := hex.EncodeToString(raw[:])

	http.SetCookie(w, stateCookie(state, int(oauthStateTTL.Seconds())))

	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", twitchClientID)
	q.Set("redirect_uri", "https://"+r.Host+"/twitch_redirect_handler")
	q.Set("scope", "channel:read:subscriptions channel:manage:redemptions moderator:read:followers")
	q.Set("state", state)

	http.Redirect(w, r, "https://id.twitch.tv/oauth2/authorize?"+q.Encode(), http.StatusFound)
}

func (api *API) logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(cookieSessionID); err == nil {
		if err := api.db.DeleteSession(r.Context(), cookie.Value); err != nil {
			api.logger.Error("failed to delete session", "err", err)
		}
	}

	http.SetCookie(w, sessionCookie("", -1))
	w.WriteHeader(http.StatusNoContent)
}

func (api *API) twitchRedirectHandler(w http.ResponseWriter, r *http.Request) {
	stateCk, err := r.Cookie(cookieOAuthState)
	if err != nil || subtle.ConstantTimeCompare([]byte(stateCk.Value), []byte(r.URL.Query().Get("state"))) != 1 {
		submitPage(w, errPage(r, http.StatusBadRequest, "login state mismatch, start the login again"))

		return
	}
	http.SetCookie(w, stateCookie("", -1))

	code := r.URL.Query().Get("code")
	if len(code) == 0 {
		submitPage(w, errPage(r, http.StatusInternalServerError, r.URL.Query().Get("error_description")))

		return
	}

	user, tokens, err := api.twitchClient.CodeHandler(code, r.Host)
	if err != nil {
		submitPage(w, errPage(r, http.StatusInternalServerError, err.Error()))

		return
	}

	id, err := api.db.UpsertUser(r.Context(), user)
	if err != nil {
		submitPage(w, errPage(r, http.StatusInternalServerError, err.Error()))

		return
	}

	user.ID = id

	if err := api.db.SetTwitchTokens(r.Context(), id, tokens); err != nil {
		submitPage(w, errPage(r, http.StatusInternalServerError, err.Error()))

		return
	}

	session, err := api.db.CreateSession(r.Context(), id)
	if err != nil {
		submitPage(w, errPage(r, http.StatusInternalServerError, err.Error()))

		return
	}

	http.SetCookie(w, sessionCookie(session, int(db.SessionTTL.Seconds())))

	if api.cfg.AutoApproveUsers {
		if _, err := api.db.AutoGrantAccess(r.Context(), user, db.PermissionStreamer); err != nil {
			api.logger.Error("auto approve user failed", "err", err, "twitch_login", user.TwitchLogin)
		}
	}

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
	modUsers, err := api.db.GetUsersPermissions(r.Context(), db.PermissionMod, db.PermissionStatusGranted)
	if err != nil {
		return getHtml("error.html", &htmlErr{
			ErrorCode:    http.StatusInternalServerError,
			ErrorMessage: fmt.Sprintf("db get mods err: %v", err),
		})
	}

	mods := make([]permissionRequest, 0, len(modUsers))
	for _, user := range modUsers {
		mods = append(mods, permissionRequest{
			Login:  user.TwitchLogin,
			UserID: user.ID,
		})
	}

	return getHtml("admin.html", &adminPage{
		Mods: mods,
	})
}

func (api *API) reloadAllOverlays(w http.ResponseWriter, r *http.Request) {
	streamers, err := api.db.GetUsersPermissions(r.Context(), db.PermissionStreamer, db.PermissionStatusGranted)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("failed to get streamers: " + err.Error()))

		return
	}

	for _, user := range streamers {
		api.connManager.ReloadOverlay(user.ID)
	}

	_, _ = fmt.Fprintf(w, "reload sent to %d overlays", len(streamers))
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

	deniedUsers, err := api.db.GetUsersWithDeniedPermission(r.Context(), db.PermissionStreamer)
	if err != nil {
		return getHtml("error.html", &htmlErr{
			ErrorCode:    http.StatusInternalServerError,
			ErrorMessage: fmt.Sprintf("db get denied users err: %v", err),
		})
	}

	denied := make([]permissionRequest, 0, len(deniedUsers))
	for _, user := range deniedUsers {
		denied = append(denied, permissionRequest{
			Login:  user.TwitchLogin,
			UserID: user.ID,
		})
	}

	return getHtml("mod.html", &modPage{
		Requests:    requests,
		Streamers:   approved,
		DeniedUsers: denied,
	})
}
