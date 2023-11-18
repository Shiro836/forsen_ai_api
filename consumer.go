package main

import (
	"net/http"
	"os"

	"app/conns"
	"app/slg"
	"app/ws"

	"log/slog"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
)

func (api *API) betaHtmlHandler(w http.ResponseWriter, r *http.Request) {
	user := chi.URLParam(r, "user")
	if !isValidUser(user, w) {
		return
	}

	data, err := os.ReadFile("client/v2.html")
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("failed to read v2.html"))

		return
	}

	w.Header().Add("Content-Type", "text/html")
	w.Write(data)
}

func (api *API) betaJsHandler(w http.ResponseWriter, r *http.Request) {
	data, err := os.ReadFile("client/v2.js")
	if err != nil {
		w.WriteHeader(500)
		w.Write([]byte("failed to read v2.js"))
	}

	w.Header().Add("Content-Type", "application/javascript")
	w.Write(data)
}

func (api *API) sendData(wsClient *ws.Client, data *conns.DataEvent) error {
	msg := &ws.Message{}

	switch data.EventType {
	case conns.EventTypeAudio:
		msg.MsgType = websocket.BinaryMessage
		msg.Message = data.EventData
	case conns.EventTypeText:
		msg.MsgType = websocket.BinaryMessage
		msg.Message = data.EventData
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

	c, err := upgrader.Upgrade(w, r, nil)
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

	dataCh := api.connectionManager.Subscribe(user)
	defer api.connectionManager.Unsubscribe(user)

	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("failed to get data stream"))

		slg.GetSlog(r.Context()).Error("failed to get data stream", "err", err)
	}

loop:
	for {
		select {
		case <-done:
			break loop
		default:
		}

		select {
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