package main

import (
	"net/http"
	"os"

	"app/ws"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
	"golang.org/x/exp/slog"
)

func (api *API) betaHtmlHandler(w http.ResponseWriter, r *http.Request) {
	user := chi.URLParam(r, "user")
	if !isValidUser(user, w) {
		return
	}

	data, err := os.ReadFile("client/beta.html")
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("failed to read beta.html"))

		return
	}

	w.Header().Add("Content-Type", "text/html")
	w.Write(data)
}

func (api *API) betaJsHandler(w http.ResponseWriter, r *http.Request) {
	data, err := os.ReadFile("client/beta.js")
	if err != nil {
		w.WriteHeader(500)
		w.Write([]byte("failed to read beta.js"))
	}

	w.Header().Add("Content-Type", "application/javascript")
	w.Write(data)
}

func (api *API) sendData(wsClient *ws.Client, data *DataEvent) error {
	return wsClient.Send(&ws.Message{
		MsgType: websocket.TextMessage,
		Message: []byte("not implemented"),
	})
}

func (api *API) consumerHandler(w http.ResponseWriter, r *http.Request) {
	user := chi.URLParam(r, "user")
	if !isValidUser(user, w) {
		return
	}

	slog := slog.With("user", user)

	c, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Error("failed to upgrade ws", "err", err)

		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("failed to upgrade ws"))

		return
	}

	wsClient, done := ws.NewWsClient(c)

	defer func() {
		slog.Info("close ws")
		wsClient.Close()
	}()

	slog.Info("ws connected")

	api.connectionManager.Subscribe(user)
	defer api.connectionManager.Unsubscribe(user)

	dataCh, err := api.connectionManager.RecieveChan(user)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("failed to get data stream"))

		slog.Error("failed to get data stream", "err", err)
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
				slog.Info("data stream ended")
				break loop
			}

			if err := api.sendData(wsClient, data); err != nil {
				slog.Error("failed to send data to ws", "err", err)
				break loop
			}
		case <-done:
			break loop
		}
	}
}
