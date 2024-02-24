package api

import (
	"app/db"
	"app/pkg/slg"
	"app/tools"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
)

type Msg struct {
	ID int `json:"id"`

	UserName       string `json:"user_name"`
	Message        string `json:"message"`
	CustomRewardID string `json:"custom_reward_id"`

	State string `json:"state"`

	Updated int `json:"updated"`
}

type Msgs struct {
	Msgs []*Msg `json:"msgs"`
}

func (api *API) GetQue(w http.ResponseWriter, r *http.Request) {
	var userData *db.UserData

	if cookie, err := r.Cookie("session_id"); err != nil || len(cookie.Value) == 0 {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte("unauthorized"))

		return
	} else if userData, err = db.GetUserDataBySessionId(cookie.Value); err != nil {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte("user data not found"))

		return
	}

	r = r.WithContext(slg.WithSlog(r.Context(), slog.With("user", userData.UserLoginData.UserName)))

	state := chi.URLParam(r, "state")
	if len(state) == 0 {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("empty state in path"))

		return
	}

	updated := chi.URLParam(r, "updated")
	if len(updated) == 0 {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("empty updated in path"))

		return
	}

	updatedInt, err := strconv.ParseInt(updated, 10, 64)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("updated is not int"))

		return
	}

	msgs, err := db.GetAllQueueMessages(userData.UserLoginData.UserId, state, int(updatedInt))
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("couldn't get messages from queue"))

		slg.GetSlog(r.Context()).Error("couldn't get messages from queue", "err", err)

		return
	}

	result := &Msgs{}

	for _, msg := range msgs {
		rewardID, _ := db.GetRewardIDFromTwitchRewardID(msg.CustomRewardID)

		result.Msgs = append(result.Msgs, &Msg{
			ID: msg.ID,

			UserName:       msg.UserName,
			Message:        msg.Message,
			CustomRewardID: rewardID,

			State: msg.State,

			Updated: msg.Updated,
		})
	}

	data, err := json.Marshal(result)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("failed to marshal json msgs"))

		slg.GetSlog(r.Context()).Error("failed to marshal json msgs", "err", err)

		return
	}

	_, _ = w.Write(data)
}

func (api *API) DeleteMsgFromQue(w http.ResponseWriter, r *http.Request) {
	var userData *db.UserData

	if cookie, err := r.Cookie("session_id"); err != nil || len(cookie.Value) == 0 {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte("unauthorized"))

		return
	} else if userData, err = db.GetUserDataBySessionId(cookie.Value); err != nil {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte("user data not found"))

		return
	}

	r = r.WithContext(slg.WithSlog(r.Context(), slog.With("user", userData.UserLoginData.UserName)))

	msgID := chi.URLParam(r, "msg_id")
	if len(msgID) == 0 {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("empty msg_id in path"))

		return
	}

	msgIdInt, err := strconv.ParseInt(msgID, 10, 64)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("msg_id is not int"))

		return
	}

	err = db.UpdateState(int(msgIdInt), tools.Deleted.String())
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("failed to update message state: " + err.Error()))

		return
	}

	api.connManager.SkipMessage(userData.UserLoginData.UserName, msgID)

	w.Write([]byte("success"))
}
