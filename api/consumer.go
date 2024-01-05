package api

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"app/conns"
	"app/slg"
	"app/ws"

	"log/slog"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
)

type frontMessage struct {
	Type string `json:"type"`
	Data string `json:"data"`
}

func (api *API) sendData(wsClient *ws.Client, data *conns.DataEvent) error {
	msg := &ws.Message{
		MsgType: websocket.BinaryMessage,
	}

	var err error

	switch data.EventType {
	case conns.EventTypeAudio:
		if msg.Message, err = json.Marshal(&frontMessage{
			Type: "audio",
			Data: base64.StdEncoding.EncodeToString(data.EventData),
		}); err != nil {
			return fmt.Errorf("failed to marshal frontMessage: %w", err)
		}
	case conns.EventTypeText:
		if msg.Message, err = json.Marshal(&frontMessage{
			Type: "text",
			Data: string(data.EventData),
		}); err != nil {
			return fmt.Errorf("failed to marshal frontMessage: %w", err)
		}
	case conns.EventTypeImage:
		if msg.Message, err = json.Marshal(&frontMessage{
			Type: "image",
			Data: string(data.EventData),
		}); err != nil {
			return fmt.Errorf("failed to marshal frontMessage: %w", err)
		}
	case conns.EventTypeSetModel:
		if msg.Message, err = json.Marshal(&frontMessage{
			Type: "model",
			Data: string(data.EventData),
		}); err != nil {
			return fmt.Errorf("failed to marshal frontMessage: %w", err)
		}
	case conns.EventTypePing:
		if msg.Message, err = json.Marshal(&frontMessage{
			Type: "ping",
			Data: "ping",
		}); err != nil {
			return fmt.Errorf("failed to marshal frontMessage: %w", err)
		}
	case conns.EventTypeInfo:
	case conns.EventTypeError:
	default:
		panic("event type not handled")
	}

	return wsClient.Send(msg)
}

func (api *API) consumerHandler(w http.ResponseWriter, r *http.Request) {
	user := chi.URLParam(r, "user")
	if !isValidUser(user, w) {
		return
	}

	r = r.WithContext(slg.WithSlog(r.Context(), slog.With("user", user)))

	slg.GetSlog(r.Context()).Info("consumer connected", "ip", r.RemoteAddr)

	c, err := ws.Upgrader.Upgrade(w, r, nil)
	if err != nil {
		slg.GetSlog(r.Context()).Error("failed to upgrade ws", "err", err)

		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("failed to upgrade ws"))

		return
	}

	wsClient, done := ws.NewWsClient(c)

	defer func() {
		slg.GetSlog(r.Context()).Info("close ws")
		wsClient.Close()
	}()

	slg.GetSlog(r.Context()).Info("ws connected")

	dataCh, unsubscribe := api.connManager.Subscribe(user)
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
			if err := api.sendData(wsClient, &conns.DataEvent{EventType: conns.EventTypePing}); err != nil {
				slg.GetSlog(r.Context()).Info("ping failed", "err", err)
				break loop
			}
		case data, ok := <-dataCh:
			if !ok {
				slg.GetSlog(r.Context()).Info("data stream ended")
				break loop
			}

			if err := api.sendData(wsClient, data); err != nil {
				slg.GetSlog(r.Context()).Error("failed to send data to ws", "err", err)
				break loop
			}
		case <-done:
			break loop
		}
	}
}
