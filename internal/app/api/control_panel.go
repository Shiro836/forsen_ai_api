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
	"github.com/nicklaw5/helix/v2"
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

type controlPanelUser struct {
	TwitchLogin  string
	TwitchUserID int
}

type controlPanel struct {
	User *controlPanelUser
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

	targetUser, err := api.db.GetUserByTwitchUserID(r.Context(), targetTwitchUserID)
	if err != nil {
		return getHtml("error.html", &htmlErr{
			ErrorCode:    http.StatusInternalServerError,
			ErrorMessage: "failed to get target user: " + err.Error(),
		})
	}

	return getHtml("control_panel.html", &controlPanel{
		User: &controlPanelUser{
			TwitchLogin:  targetUser.TwitchLogin,
			TwitchUserID: targetUser.TwitchUserID,
		},
	})
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

	targetUser, err := api.db.GetUserByTwitchUserID(r.Context(), targetTwitchUserID)
	if err != nil {
		api.logger.Error("failed to get target user", "err", err, "user", user.TwitchLogin, "target_twitch_user_id", targetTwitchUserID)

		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("failed to get target user: " + err.Error()))

		return
	}

	logger := api.logger.With("user", user.TwitchLogin, "target_user", targetUser.TwitchLogin)

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

	events := api.controlPanelNotifications.SubscribeForNotification(r.Context(), targetUser.ID)

	ticker := time.NewTicker(2 * time.Minute)
	defer ticker.Stop()

	lastUpdated := 0

	lastActive := time.Now()

	go func() {
		defer wsClient.Close()

	read_loop:
		for {
			msg, err := wsClient.Read()
			if err != nil {
				logger.Error("failed to read control panel websocket message", "err", err)
				break read_loop
			}

			var upd *skipMsg
			err = json.Unmarshal(msg.Message, &upd)
			if err != nil {
				logger.Error("failed to unmarshal message from ws", "err", err)
				break read_loop
			}

			api.connManager.SkipMessage(targetUser.ID, upd.ID)
		}
	}()

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

		dbMessages, err := api.db.GetMessageUpdates(r.Context(), targetUser.ID, lastUpdated)
		if err != nil {
			logger.Error("failed to get message updates", "err", err)
			break loop
		}

		if len(dbMessages) == 0 {
			if time.Since(lastActive) > 20*time.Second {
				err = wsClient.Send(&ws.Message{
					MsgType: websocket.BinaryMessage,
					Message: []byte("ping"),
				})
				if err != nil {
					logger.Error("failed to ping", "err", err)
					break loop
				}
				lastActive = time.Now()
			}

			continue
		}

		updates := make([]Update, 0, len(dbMessages))
		for _, dbMessage := range dbMessages {
			lastUpdated = max(lastUpdated, dbMessage.Updated)
			var data []byte

			var action Action

			switch dbMessage.Status {
			case db.MsgStatusProcessed, db.MsgStatusDeleted:
				action = ActionDelete
				data, err = json.Marshal(&msgDelete{
					ID: dbMessage.ID.String(),
				})
				if err != nil {
					logger.Error("failed to marshal message", "err", err)
					break loop
				}
			case db.MsgStatusCurrent, db.MsgStatusWait:
				action = ActionUpsert
				charCard, rewardType, err := api.db.GetCharCardByTwitchReward(r.Context(), user.ID, dbMessage.TwitchMessage.RewardID)
				if err != nil {
					logger.Error("failed to get char card by twitch reward", "err", err)
					break loop
				}

				var rewardTypeStr string = "unknown"
				switch rewardType {
				case db.TwitchRewardTTS:
					rewardTypeStr = "TTS"
				case db.TwitchRewardAI:
					rewardTypeStr = "AI"
				}

				msgData, err := db.ParseMessageData(dbMessage.Data)
				if err != nil {
					logger.Error("failed to parse message data", "err", err)
					break loop
				}

				data, err = json.Marshal(&msgUpsert{
					ID: dbMessage.ID.String(),

					RequestedBy: dbMessage.TwitchMessage.TwitchLogin,

					Type:     rewardTypeStr,
					CharName: charCard.Name,

					Request:  dbMessage.TwitchMessage.Message,
					Response: msgData.AIResponse,

					Status: dbMessage.Status.String(),
				})
				if err != nil {
					logger.Error("failed to marshal message", "err", err)
					break loop
				}
			}

			updates = append(updates, Update{
				Action: action,
				Data:   data,
			})
		}

		messages, err := json.Marshal(&Updates{
			Updates: updates,
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
		lastActive = time.Now()
	}
}

type msgDelete struct {
	ID string `json:"id"`
}

type msgUpsert struct {
	ID string `json:"id"`

	RequestedBy string `json:"requested_by"`

	Type     string `json:"type"`
	CharName string `json:"char_name"`

	Request  string `json:"request"`
	Response string `json:"response"`

	Status string `json:"status"`
}

type Action int

const (
	ActionDelete Action = iota
	ActionUpsert
)

type Update struct {
	Action Action `json:"action"`
	Data   []byte `json:"data"`
}

type Updates struct {
	ClearAll bool     `json:"clear_all"`
	Updates  []Update `json:"updates"`
}

type skipMsg struct {
	ID string `json:"id"`
}

func (api *API) controlPanelGrant(w http.ResponseWriter, r *http.Request) {
	user := ctxstore.GetUser(r.Context())
	if user == nil {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("unauthorized"))
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

	twitchAPI, err := api.twitchClient.NewHelixClient(user.TwitchAccessToken, user.TwitchRefreshToken) // TODO: generalize this
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

	targetUserID, err := strconv.Atoi(resp.Data.Users[0].ID)
	if err != nil {
		_ = html.ExecuteTemplate(w, "error.html", &htmlErr{
			ErrorCode:    http.StatusInternalServerError,
			ErrorMessage: fmt.Sprintf("twitch get users err: %v", err),
		})

		return
	}

	_, err = api.db.AddRelation(r.Context(), &db.Relation{
		TwitchLogin1:  targetLogin,
		TwitchUserID1: targetUserID,

		TwitchLogin2:  user.TwitchLogin,
		TwitchUserID2: user.TwitchUserID,

		RelationType: db.RelationTypeModerating,
	})
	if err != nil {
		_ = html.ExecuteTemplate(w, "error.html", &htmlErr{
			ErrorCode:    http.StatusInternalServerError,
			ErrorMessage: fmt.Sprintf("failed to add relation: %v", err),
		})

		return
	}

	_, _ = w.Write([]byte("success"))
}

func (api *API) controlPanelRevoke(w http.ResponseWriter, r *http.Request) {
	_, _ = w.Write([]byte("not implemented"))
}

// add any relation
func (api *API) adminControlPanelGrant(w http.ResponseWriter, r *http.Request) {
	user := ctxstore.GetUser(r.Context())
	if user == nil {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("unauthorized"))
		return
	}

	_, _ = w.Write([]byte("not implemented"))
}
