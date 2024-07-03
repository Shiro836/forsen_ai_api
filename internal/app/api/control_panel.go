package api

import (
	"app/db"
	"app/pkg/ctxstore"
	"app/pkg/ws"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
)

type PanelData struct {
	TwitchLogin  string
	TwitchUserID int
}

type controlPanelMenu struct {
	Panels []PanelData
}

func (api *API) controlPanelMenu(r *http.Request) template.HTML {
	user := ctxstore.GetUser(r.Context())
	if user == nil {
		return getHtml("error.html", &htmlErr{
			ErrorCode:    http.StatusInternalServerError,
			ErrorMessage: "unauthorized",
		})
	}

	relations, err := api.db.GetRelations(r.Context(), user, db.RelationTypeModerating)
	if err != nil {
		return getHtml("error.html", &htmlErr{
			ErrorCode:    http.StatusInternalServerError,
			ErrorMessage: "failed to get relations: " + err.Error(),
		})
	}

	panels := make([]PanelData, 0, len(relations)+1)

	hasPerm, err := api.db.HasPermission(r.Context(), user.TwitchUserID, db.PermissionStreamer)
	if err != nil {
		return getHtml("error.html", &htmlErr{
			ErrorCode:    http.StatusInternalServerError,
			ErrorMessage: "failed to check permission: " + err.Error(),
		})
	}

	if hasPerm {
		panels = append(panels, PanelData{
			TwitchLogin:  user.TwitchLogin,
			TwitchUserID: user.TwitchUserID,
		})
	}

	for _, relation := range relations {
		panels = append(panels, PanelData{
			TwitchLogin:  relation.TwitchLogin2,
			TwitchUserID: relation.TwitchUserID2,
		})
	}

	return getHtml("control_panel_menu.html", &controlPanelMenu{
		Panels: panels,
	})
}

func getTwitchUserID(r *http.Request) (int, error) {
	twitchUserIDStr := chi.URLParam(r, "twitch_user_id")
	if len(twitchUserIDStr) == 0 {
		return 0, fmt.Errorf("empty twitch user id")
	}

	twitchUserID, err := strconv.Atoi(twitchUserIDStr)
	if err != nil {
		return 0, fmt.Errorf("twitch user id is not int: %w", err)
	}

	return twitchUserID, nil
}

func (api *API) hasControlPanelPermissions(user *db.User, targetTwitchUserID int, r *http.Request) (bool, error) {
	if targetTwitchUserID != user.TwitchUserID {
		relations, err := api.db.GetRelations(r.Context(), user, db.RelationTypeModerating)
		if err != nil {
			return false, fmt.Errorf("failed to get relations: %w", err)
		}

		for _, relation := range relations {
			if relation.TwitchUserID2 == targetTwitchUserID {
				return true, nil
			}
		}

		return false, nil
	}

	return true, nil
}

func (api *API) controlPanel(r *http.Request) template.HTML {
	user := ctxstore.GetUser(r.Context())
	if user == nil {
		return getHtml("error.html", &htmlErr{
			ErrorCode:    http.StatusInternalServerError,
			ErrorMessage: "unauthorized",
		})
	}

	targetTwitchUserID, err := getTwitchUserID(r)
	if err != nil {
		return getHtml("error.html", &htmlErr{
			ErrorCode:    http.StatusInternalServerError,
			ErrorMessage: "failed to get twitch user id: " + err.Error(),
		})
	}

	if hasPerm, err := api.hasControlPanelPermissions(user, targetTwitchUserID, r); err != nil {
		return getHtml("error.html", &htmlErr{
			ErrorCode:    http.StatusInternalServerError,
			ErrorMessage: "failed to check permission: " + err.Error(),
		})
	} else if !hasPerm {
		return getHtml("error.html", &htmlErr{
			ErrorCode:    http.StatusInternalServerError,
			ErrorMessage: "you are not moderating this user",
		})
	}

	return getHtml("control_panel.html", nil)
}

func (api *API) controlPanelWSConn(w http.ResponseWriter, r *http.Request) {
	user := ctxstore.GetUser(r.Context())
	if user == nil {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("unauthorized"))

		return
	}

	targetTwitchUserID, err := getTwitchUserID(r)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("failed to get twitch user id: " + err.Error()))

		return
	}

	logger := api.logger.With("user", user.TwitchLogin, "target_twitch_user_id", targetTwitchUserID)

	if hasPerm, err := api.hasControlPanelPermissions(user, targetTwitchUserID, r); err != nil {
		logger.Error("failed to check permission", "err", err)

		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("failed to check permission: " + err.Error()))

		return
	} else if !hasPerm {
		logger.Error("you are not moderating this user")

		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("you are not moderating this user"))

		return
	}

	targetUser, err := api.db.GetUserByTwitchUserID(r.Context(), targetTwitchUserID)
	if err != nil {
		logger.Error("failed to get target user", "err", err)

		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("failed to get target user: " + err.Error()))

		return
	}

	wsConn, err := ws.Upgrader.Upgrade(w, r, nil)
	if err != nil {
		logger.Error("failed to upgrade control panel websocket connection", "err", err)
		w.WriteHeader(http.StatusInternalServerError)

		return
	}

	wsClient, done := ws.NewWsClient(wsConn)

	defer func() {
		logger.Info("closing control panel websocket connection")
		wsClient.Close()
	}()

	messages, err := json.Marshal(&Updates{
		Updates: []Update{
			{
				Action:  ActionUpsert,
				Message: fmt.Sprintf("Test: %s, %s", user.TwitchLogin, targetUser.TwitchLogin),
			},
		},
	})
	if err != nil {
		logger.Error("failed to marshal messages", "err", err)

		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("failed to marshal messages: " + err.Error()))

		return
	}

	err = wsClient.Send(&ws.Message{
		MsgType: websocket.BinaryMessage,
		Message: messages,
	})
	if err != nil {
		logger.Error("failed to send message", "err", err)

		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("failed to send message: " + err.Error()))

		return
	}

	events := api.controlPanelNotifications.SubscribeForNotification(r.Context(), targetUser.ID)

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

loop:
	for {
		select {
		case <-done:
			break loop
		case _, ok := <-events:
			if !ok {
				break loop
			}
		case <-ticker.C:
		}

		if err := wsClient.Send(&ws.Message{
			MsgType: websocket.BinaryMessage,
			Message: nil,
		}); err != nil {
			logger.Error("failed to send message", "err", err)
		}
	}
}

type Action int

const (
	ActionDelete Action = iota
	ActionUpsert
)

type Update struct {
	Action  Action `json:"action"`
	Message string `json:"message"`
}

type Updates struct {
	ClearAll bool     `json:"clear_all"`
	Updates  []Update `json:"updates"`
}
