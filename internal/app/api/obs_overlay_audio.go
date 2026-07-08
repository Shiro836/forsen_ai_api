package api

import (
	"app/db"
	"app/internal/app/conns"
	"app/pkg/ws"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
)

// wsAudioHandler serves the overlay's dedicated audio socket: binary chunk /
// track_done frames only (overlay-v2). Control, text and client actions stay
// on the JSON socket, so a stalled audio pipe can never delay a skip.
func (api *API) wsAudioHandler(w http.ResponseWriter, r *http.Request) {
	twitchLogin := chi.URLParam(r, "twitch_login")
	if len(twitchLogin) == 0 {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("missing twitch login"))
		return
	}

	logger := api.logger.With("user", twitchLogin, "conn", "audio")

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

	wsConn, err := ws.Upgrader.Upgrade(w, r, nil)
	if err != nil {
		logger.Error("failed to upgrade to websocket connection", "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	wsClient, done := ws.NewWsClient(wsConn)
	defer func() {
		logger.Info("closing audio websocket connection")
		wsClient.Close()
	}()

	logger.Info("audio websocket connection established")

	frames, unsubscribe := api.connManager.SubscribeAudio(user.ID)
	defer unsubscribe()

	// the client sends nothing meaningful here, but the connection dies without
	// a read pump draining control frames
	go wsClient.DrainRead()

	t := time.NewTicker(3 * time.Second)
	defer t.Stop()

	sendBinary := func(frame []byte) error {
		return wsClient.Send(&ws.Message{
			MsgType: websocket.BinaryMessage,
			Message: frame,
		})
	}

loop:
	for {
		select {
		case <-done:
			break loop
		default:
		}

		select {
		case <-t.C:
			if err := sendBinary([]byte{conns.AudioFramePing}); err != nil {
				logger.Error("failed to send audio ping", "err", err)
				break loop
			}
		case frame, ok := <-frames:
			if !ok {
				logger.Info("audio channel closed")
				break loop
			}
			if err := sendBinary(frame); err != nil {
				logger.Error("failed to send audio frame", "err", err)
				break loop
			}
		case <-done:
			break loop
		}
	}
}
