package api

import (
	"app/db"
	"app/internal/app/conns"
	"app/pkg/ws"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"sync"
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
		JSVersion   string
	}{
		TwitchLogin: twitchLogin,
		JSVersion:   overlayJSVersion(),
	})
}

// overlayJSVersion is a content hash of every overlay asset; the script and
// stylesheet tags carry it as ?v= so a remote-triggered reload actually
// fetches new files instead of the OBS browser-source cache — a CSS-only
// tweak must bust the cache just like a JS change.
var overlayJSVersion = sync.OnceValue(func() string {
	h := sha256.New()
	for _, name := range []string{
		"static/obs-overlay.js",
		"static/overlay-player.js",
		"static/obs-overlay.css",
		"static/overlay-player.css",
	} {
		data, err := staticFS.ReadFile(name)
		if err != nil {
			return "dev"
		}
		h.Write(data)
	}
	return hex.EncodeToString(h.Sum(nil)[:8])
})

type obsAction struct {
	Action string `json:"action"`
	Token  string `json:"token"`
	MsgID  string `json:"msg_id"`
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

	// first frame: resync a (re)connecting overlay — restores pending_skips
	// before any late chunk of a skipped message can play (see overlay-v2 ADR,
	// skip-vs-reconnect race)
	skipped, current := api.connManager.OverlaySnapshot(user.ID)
	snapshot, _ := json.Marshal(struct {
		Skipped      []string `json:"skipped"`
		CurrentMsgID string   `json:"current_msg_id"`
	}{Skipped: skipped, CurrentMsgID: current})
	if err := sendData(wsClient, conns.EventTypeSnapshot.String(), snapshot); err != nil {
		logger.Error("failed to send snapshot", "err", err)
	}

	// READ FROM WS
	go func() {
		defer wsClient.Close()
		for {
			msg, err := wsClient.Read()
			if err != nil {
				if !errors.Is(err, ws.ErrClosed) {
					logger.Error("failed to read from ws", "err", err)
				}

				break
			}

			var upd *obsAction
			err = json.Unmarshal(msg.Message, &upd)
			if err != nil {
				logger.Error("failed to unmarshal message from ws", "err", err)
			}

			switch upd.Action {
			case "skip":
				api.connManager.SkipCurrent(user.ID, upd.Token, upd.MsgID)
			case "show_images":
				api.connManager.ShowImagesCurrent(user.ID, upd.Token, upd.MsgID)
			default:
				logger.Error("unknown action", "action", upd.Action)
			}
		}
	}()

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
