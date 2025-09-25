package api

import (
	"app/db"
	"app/internal/app/conns"
	"app/pkg/ws"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
)

func (api *API) obsOverlay(r *http.Request) template.HTML {
	twitchLogin := chi.URLParam(r, "twitch_login")
	if len(twitchLogin) == 0 {
		return getHtml("error.html", &htmlErr{
			ErrorCode:    http.StatusBadRequest,
			ErrorMessage: "missing twitch login",
		})
	}

	twitchUser, err := api.db.GetUserByTwitchLogin(r.Context(), twitchLogin)
	if err != nil {
		return getHtml("error.html", &htmlErr{
			ErrorCode:    http.StatusNotFound,
			ErrorMessage: "user not found" + err.Error(),
		})
	}

	if hasPerm, _, err := api.db.HasPermission(r.Context(), twitchUser.TwitchUserID, db.PermissionStreamer); err != nil {
		return getHtml("error.html", &htmlErr{
			ErrorCode:    http.StatusInternalServerError,
			ErrorMessage: "failed to check permission",
		})
	} else if !hasPerm {
		return getHtml("error.html", &htmlErr{
			ErrorCode:    http.StatusForbidden,
			ErrorMessage: twitchLogin + " doesn't have permission",
		})
	}

	return getHtml("obs_overlay.html", struct {
		TwitchLogin string
	}{
		TwitchLogin: twitchLogin,
	})
}

func (api *API) wsHandler(w http.ResponseWriter, r *http.Request) {
	twitchLogin := chi.URLParam(r, "twitch_login")
	if len(twitchLogin) == 0 {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("missing twitch login"))

		return
	}

	logger := api.logger.With("user", twitchLogin)

	user, err := api.db.GetUserByTwitchLogin(r.Context(), twitchLogin)
	if err != nil {
		logger.Error("failed to get user", "err", err)

		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("user not found"))

		return
	}

	if hasPerm, _, err := api.db.HasPermission(r.Context(), user.TwitchUserID, db.PermissionStreamer); err != nil {
		logger.Error("failed to check permission", "err", err)

		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("failed to check permission"))

		return
	} else if !hasPerm {
		logger.Info("no permission")

		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("you don't have permission"))

		return
	}

	logger.Info("received websocket connection request")

	wsConn, err := ws.Upgrader.Upgrade(w, r, nil)
	if err != nil {
		logger.Error("failed to upgrade to websocket connection", "err", err)
		w.WriteHeader(http.StatusInternalServerError)

		return
	}

	wsClient, done := ws.NewWsClient(wsConn)

	defer func() {
		logger.Info("closing websocket connection")
		wsClient.Close()
	}()

	logger.Info("websocket connection established")

	dataCh, unsubscribe := api.connManager.Subscribe(user.ID)
	defer unsubscribe()

	t := time.NewTicker(3 * time.Second)
	defer t.Stop()

loop:
	for {
		select {
		case <-done:
			break loop
		default:
		}

		select {
		case <-t.C:
			if err := sendData(wsClient, conns.EventTypePing.String(), []byte("ping")); err != nil {
				logger.Error("failed to send ping to ws conn", "err", err)
				break loop
			}
		case data, ok := <-dataCh:
			if !ok {
				logger.Info("data channel closed")
				break loop
			}

			if err := sendData(wsClient, data.EventType.String(), data.EventData); err != nil {
				logger.Error("failed to send data to ws conn", "err", err)
				break loop
			}
		case <-done:
			break loop
		}
	}
}

func sendData(wsClient *ws.Client, eventType string, data []byte) error {
	msg, err := json.Marshal(struct {
		Type string `json:"type"`
		Data []byte `json:"data"`
	}{
		Type: eventType,
		Data: data,
	})
	if err != nil {
		return fmt.Errorf("failed to marshal data: %w", err)
	}

	if err := wsClient.Send(&ws.Message{
		MsgType: websocket.BinaryMessage,
		Message: msg,
	}); err != nil {
		return fmt.Errorf("failed to send data to ws conn: %w", err)
	}

	return nil
}
