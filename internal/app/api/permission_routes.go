package api

import (
	"app/db"
	"app/pkg/ctxstore"
	"fmt"
	"net/http"
	"strconv"

	"github.com/google/uuid"
	"github.com/nicklaw5/helix/v2"
)

type permissionAction int

const (
	permissionActionAdd permissionAction = iota
	permissionActionRemove
)

func (permissionAction permissionAction) String() string {
	switch permissionAction {
	case permissionActionAdd:
		return "add"
	case permissionActionRemove:
		return "remove"
	default:
		return "unknown"
	}
}

func authPage(r *http.Request) *page {
	page := createPage(r)
	page.Content = getHtml("index.html", &LoginPage{
		RedirectUrl: "https://id.twitch.tv/oauth2/authorize?response_type=code&client_id=zi6vy3y3iq38svpmlub5fd26uwsee8&redirect_uri=https://" + r.Host + "/twitch_redirect_handler&scope=channel:read:subscriptions+channel:manage:redemptions+moderator:read:followers",
	})

	return page
}

func (api *API) managePermission(permissionAction permissionAction, permission db.Permission) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		initiatorUser := ctxstore.GetUser(r.Context())
		if initiatorUser == nil {
			submitPage(w, authPage(r))

			return
		}

		targetUserIDStr := r.FormValue("user_id")
		if len(targetUserIDStr) != 0 {
			targetUserID, err := uuid.Parse(targetUserIDStr)
			if err != nil {
				_ = html.ExecuteTemplate(w, "error.html", &htmlErr{
					ErrorCode:    http.StatusBadRequest,
					ErrorMessage: fmt.Sprintf("user_id is not valid uuid: %v", err),
				})

				return
			}

			targetUser, err := api.db.GetUserByID(r.Context(), targetUserID)
			if err != nil {
				_ = html.ExecuteTemplate(w, "error.html", &htmlErr{
					ErrorCode:    http.StatusInternalServerError,
					ErrorMessage: fmt.Sprintf("get user by id err: %v", err),
				})

				return
			}

			defer func() {
				if permission == db.PermissionStreamer && permissionAction == permissionActionAdd {
					_ = api.handleNewUser(r.Context(), targetUser)
				}
			}()

			// TODO: stop processor

			switch permissionAction {
			case permissionActionAdd:
				if err = api.db.AddPermission(r.Context(), initiatorUser, targetUser.TwitchUserID, targetUser.TwitchLogin, permission); err != nil {
					_ = html.ExecuteTemplate(w, "error.html", &htmlErr{
						ErrorCode:    http.StatusInternalServerError,
						ErrorMessage: fmt.Sprintf("db %s permission err: %v", permissionAction.String(), err),
					})

					return
				}
			case permissionActionRemove:
				if err = api.db.RemovePermission(r.Context(), initiatorUser, targetUser.TwitchUserID, permission); err != nil {
					_ = html.ExecuteTemplate(w, "error.html", &htmlErr{
						ErrorCode:    http.StatusInternalServerError,
						ErrorMessage: fmt.Sprintf("db add permission err: %v", err),
					})

					return
				}
			default:
				_ = html.ExecuteTemplate(w, "error.html", &htmlErr{
					ErrorCode:    http.StatusInternalServerError,
					ErrorMessage: fmt.Sprintf("Invalid permission action: %v", permissionAction),
				})

				return
			}

			_, _ = w.Write([]byte("Success"))
			return
		}

		targetLogin := r.FormValue("twitch_login")
		if len(targetLogin) == 0 {
			targetLogin = r.FormValue("twitch_login_2")
		}
		if len(targetLogin) == 0 {
			_ = html.ExecuteTemplate(w, "error.html", &htmlErr{
				ErrorCode:    http.StatusBadRequest,
				ErrorMessage: "No user provided",
			})

			return
		}

		twitchAPI, err := api.twitchClient.NewHelixClient(initiatorUser.TwitchAccessToken, initiatorUser.TwitchRefreshToken)
		if err != nil {
			_ = html.ExecuteTemplate(w, "error.html", &htmlErr{
				ErrorCode:    http.StatusInternalServerError,
				ErrorMessage: "twitch client err: " + err.Error(),
			})

			return
		}

		resp, err := twitchAPI.GetUsers(&helix.UsersParams{
			Logins: []string{
				targetLogin,
			},
		})
		if err != nil {
			_ = html.ExecuteTemplate(w, "error.html", &htmlErr{
				ErrorCode:    http.StatusInternalServerError,
				ErrorMessage: fmt.Sprintf("twitch get users err: %v", err),
			})

			return
		}
		if resp == nil || len(resp.Data.Users) == 0 {
			_, _ = w.Write([]byte("user not found"))

			return
		}

		targetTwitchUserID, err := strconv.Atoi(resp.Data.Users[0].ID)
		if err != nil {
			_ = html.ExecuteTemplate(w, "error.html", &htmlErr{
				ErrorCode:    http.StatusInternalServerError,
				ErrorMessage: fmt.Sprintf("twitch get users err: %v", err),
			})

			return
		}

		defer func() {
			if permission == db.PermissionStreamer && permissionAction == permissionActionAdd {
				_ = api.handleNewTwitchUserID(r.Context(), targetTwitchUserID)
			}
		}()

		switch permissionAction {
		case permissionActionAdd:
			if err = api.db.AddPermission(r.Context(), initiatorUser, targetTwitchUserID, resp.Data.Users[0].Login, permission); err != nil {
				_ = html.ExecuteTemplate(w, "error.html", &htmlErr{
					ErrorCode:    http.StatusInternalServerError,
					ErrorMessage: fmt.Sprintf("db add permission err: %v", err),
				})

				return
			}
		case permissionActionRemove:
			if err = api.db.RemovePermission(r.Context(), initiatorUser, targetTwitchUserID, permission); err != nil {
				_ = html.ExecuteTemplate(w, "error.html", &htmlErr{
					ErrorCode:    http.StatusInternalServerError,
					ErrorMessage: fmt.Sprintf("db add permission err: %v", err),
				})

				return
			}
		default:
			_ = html.ExecuteTemplate(w, "error.html", &htmlErr{
				ErrorCode:    http.StatusBadRequest,
				ErrorMessage: fmt.Sprintf("Invalid permission action: %d", permissionAction),
			})

			return
		}

		_, _ = w.Write([]byte("Success"))
	}
}

func (api *API) manageRelation(action permissionAction, relationType db.RelationType) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		initiatorUser := ctxstore.GetUser(r.Context())
		if initiatorUser == nil {
			submitPage(w, authPage(r))

			return
		}

		fromUserLogin := r.FormValue("from_user")
		toUserLogin := r.FormValue("to_user")

		if len(fromUserLogin) == 0 || len(toUserLogin) == 0 {
			_ = html.ExecuteTemplate(w, "error.html", &htmlErr{
				ErrorCode:    http.StatusBadRequest,
				ErrorMessage: "Both users must be provided",
			})

			return
		}

		twitchAPI, err := api.twitchClient.NewHelixClient(initiatorUser.TwitchAccessToken, initiatorUser.TwitchRefreshToken)
		if err != nil {
			_ = html.ExecuteTemplate(w, "error.html", &htmlErr{
				ErrorCode:    http.StatusInternalServerError,
				ErrorMessage: "twitch client err: " + err.Error(),
			})

			return
		}

		// Get both users
		resp, err := twitchAPI.GetUsers(&helix.UsersParams{
			Logins: []string{
				fromUserLogin,
				toUserLogin,
			},
		})
		if err != nil {
			_ = html.ExecuteTemplate(w, "error.html", &htmlErr{
				ErrorCode:    http.StatusInternalServerError,
				ErrorMessage: fmt.Sprintf("twitch get users err: %v", err),
			})

			return
		}
		if resp == nil || len(resp.Data.Users) != 2 {
			_ = html.ExecuteTemplate(w, "error.html", &htmlErr{
				ErrorCode:    http.StatusBadRequest,
				ErrorMessage: "One or both users not found",
			})

			return
		}

		var fromUser, toUser helix.User
		for _, u := range resp.Data.Users {
			if u.Login == fromUserLogin {
				fromUser = u
			} else if u.Login == toUserLogin {
				toUser = u
			}
		}

		fromUserID, err := strconv.Atoi(fromUser.ID)
		if err != nil {
			_ = html.ExecuteTemplate(w, "error.html", &htmlErr{
				ErrorCode:    http.StatusInternalServerError,
				ErrorMessage: fmt.Sprintf("failed to parse from user id: %v", err),
			})

			return
		}

		toUserID, err := strconv.Atoi(toUser.ID)
		if err != nil {
			_ = html.ExecuteTemplate(w, "error.html", &htmlErr{
				ErrorCode:    http.StatusInternalServerError,
				ErrorMessage: fmt.Sprintf("failed to parse to user id: %v", err),
			})

			return
		}

		relation := &db.Relation{
			TwitchLogin1:  fromUser.Login,
			TwitchUserID1: fromUserID,

			TwitchLogin2:  toUser.Login,
			TwitchUserID2: toUserID,

			RelationType: relationType,
		}

		switch action {
		case permissionActionAdd:
			_, err = api.db.AddRelation(r.Context(), relation)
			if err != nil {
				_ = html.ExecuteTemplate(w, "error.html", &htmlErr{
					ErrorCode:    http.StatusInternalServerError,
					ErrorMessage: fmt.Sprintf("failed to add relation: %v", err),
				})

				return
			}
		case permissionActionRemove:
			err = api.db.RemoveRelation(r.Context(), relation)
			if err != nil {
				_ = html.ExecuteTemplate(w, "error.html", &htmlErr{
					ErrorCode:    http.StatusInternalServerError,
					ErrorMessage: fmt.Sprintf("failed to remove relation: %v", err),
				})

				return
			}
		}

		_, _ = w.Write([]byte("Success"))
	}
}
