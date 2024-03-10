package api

import (
	"app/db"
	"app/pkg/ctxstore"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

func (api *API) nav(getContent func(r *http.Request) template.HTML) http.HandlerFunc {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user := ctxstore.GetUser(r.Context())

		page := createPage(r)

		if user == nil {
			submitPage(w, authPage(r))

			return
		}

		userPermissions, err := api.db.GetUserPermissions(r.Context(), user.ID, db.StatusGranted)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			submitPage(w, errPage(r, http.StatusInternalServerError, err.Error()))

			return
		}

		navPage := &navPage{
			Content: getContent(r),
		}
		for _, permission := range userPermissions {
			switch permission {
			case db.PermissionStreamer:
				navPage.IsStreamer = true
			case db.PermissionMod:
				navPage.IsMod = true
			case db.PermissionAdmin:
				navPage.IsAdmin = true
			}
		}

		page.Content = getHtml("navbar.html", navPage)
		submitPage(w, page)
	})
}

func (api *API) twitchRedirectHandler(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	if len(code) == 0 {
		w.WriteHeader(http.StatusInternalServerError)
		submitPage(w, errPage(r, http.StatusInternalServerError, r.URL.Query().Get("error_description")))

		return
	}

	twitchUser, err := api.twitchClient.CodeHandler(code)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		submitPage(w, errPage(r, http.StatusInternalServerError, err.Error()))

		return
	}

	twitchUser.Session = uuid.NewString()

	_, err = api.db.UpsertUser(r.Context(), twitchUser)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		submitPage(w, errPage(r, http.StatusInternalServerError, err.Error()))

		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:  cookieSessionID,
		Value: twitchUser.Session,

		Path:    "/",
		Expires: time.Now().Add(time.Hour * 24 * 365),

		Secure: true,
	})

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (api *API) home(r *http.Request) template.HTML {
	user := ctxstore.GetUser(r.Context())
	if user == nil {
		return getHtml("error.html", &htmlErr{
			ErrorCode:    http.StatusInternalServerError,
			ErrorMessage: "no user found, very unlucky",
		})
	}

	return getHtml("home.html", &homePage{
		URL: r.Host + "/" + user.TwitchLogin,
	})
}

func (api *API) filters(r *http.Request) template.HTML {
	return getHtml("filters.html", nil)
}

func (api *API) requestPermissions(w http.ResponseWriter, r *http.Request) {
	permissionStr := chi.URLParam(r, "permission")
	permissionInt, err := strconv.Atoi(permissionStr)
	permission := db.Permission(permissionInt)

	if err != nil || !db.IsValidPermission(permission) {
		w.WriteHeader(http.StatusBadRequest)
		_ = html.ExecuteTemplate(w, "error.html", &htmlErr{
			ErrorCode:    http.StatusBadRequest,
			ErrorMessage: "Invalid permission provided",
		})

		return
	}

	user := ctxstore.GetUser(r.Context())
	if user == nil {
		w.WriteHeader(http.StatusForbidden)
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

		w.WriteHeader(http.StatusInternalServerError)
		_ = html.ExecuteTemplate(w, "error.html", &htmlErr{
			ErrorCode:    http.StatusInternalServerError,
			ErrorMessage: err.Error(),
		})

		return
	}

	_, _ = w.Write([]byte("Requested"))
}

func (api *API) mod(r *http.Request) template.HTML {
	requestUsers, err := api.db.GetUsersPermissions(r.Context(), db.PermissionStreamer, db.StatusWaiting)
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

	approvedUsers, err := api.db.GetUsersPermissions(r.Context(), db.PermissionStreamer, db.StatusGranted)
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

func (api *API) admin(r *http.Request) template.HTML {
	return getHtml("admin.html", nil)
}
