package api

import (
	"app/db"
	"app/internal/app/conns"
	"app/internal/app/processor"
	"app/pkg/ctxstore"
	"app/pkg/ws"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

type charsElem struct {
	Characters []*charElem
	IsAdmin    bool
}

type charElem struct {
	Card *db.Card

	CanEdit bool
	IsAdmin bool

	TTSRewardCreated bool
	AIRewardCreated  bool

	Author string
}

func (api *API) characters(r *http.Request) template.HTML {
	user := ctxstore.GetUser(r.Context())
	if user == nil {
		return getHtml("error.html", &htmlErr{
			ErrorCode:    http.StatusInternalServerError,
			ErrorMessage: "unauthorized",
		})
	}

	charCards, err := api.db.GetCharCards(r.Context(), user.ID, db.GetChatCardsParams{
		ShowPublic: true,
		SortBy:     db.SortByNewest,
	})
	if err != nil {
		return getHtml("error.html", &htmlErr{
			ErrorCode:    http.StatusInternalServerError,
			ErrorMessage: "GetCharCards: " + err.Error(),
		})
	}

	// existingRewards := make(map[string]struct{}, len(charCards)) // TODO: cache this
	// twitchClient, err := api.twitchClient.NewHelixClient(user.TwitchAccessToken, user.TwitchRefreshToken)
	// if err != nil {
	// 	return getHtml("error.html", &htmlErr{
	// 		ErrorCode:    http.StatusInternalServerError,
	// 		ErrorMessage: "NewHelixClient: " + err.Error(),
	// 	})
	// }

	// resp, err := twitchClient.GetCustomRewards(&helix.GetCustomRewardsParams{
	// 	BroadcasterID:         strconv.Itoa(user.TwitchUserID),
	// 	OnlyManageableRewards: true,
	// })
	// if err == nil {
	// 	for _, reward := range resp.Data.ChannelCustomRewards {
	// 		existingRewards[reward.ID] = struct{}{}
	// 	}
	// } else {
	// 	fmt.Println(err) // TODO: log
	// }

	chars := make([]*charElem, 0, len(charCards))
	isAdmin := false
	if perms, err := api.db.GetUserPermissions(r.Context(), user.ID, db.PermissionStatusGranted); err == nil {
		for _, p := range perms {
			if p == db.PermissionAdmin {
				isAdmin = true
				break
			}
		}
	}
	for _, charCard := range charCards {
		author := ""
		if owner, err := api.db.GetUserByID(r.Context(), charCard.OwnerUserID); err == nil && owner != nil {
			author = owner.TwitchLogin
		}

		chars = append(chars, &charElem{
			Card:             charCard,
			CanEdit:          user.ID == charCard.OwnerUserID,
			IsAdmin:          isAdmin,
			TTSRewardCreated: false,
			Author:           author,
		})
	}

	return getHtml("characters.html", &charsElem{
		Characters: chars,
	})
}

func (api *API) updateShortCharName(w http.ResponseWriter, r *http.Request) {
	user := ctxstore.GetUser(r.Context())
	if user == nil {
		_ = html.ExecuteTemplate(w, "error.html", &htmlErr{
			ErrorCode:    http.StatusUnauthorized,
			ErrorMessage: "not authorized",
		})
		return
	}

	characterIDStr := chi.URLParam(r, "character_id")
	characterID, err := uuid.Parse(characterIDStr)
	if err != nil {
		_ = html.ExecuteTemplate(w, "error.html", &htmlErr{
			ErrorCode:    http.StatusBadRequest,
			ErrorMessage: "character_id is not a valid uuid",
		})
		return
	}

	if err := r.ParseForm(); err != nil {
		_ = html.ExecuteTemplate(w, "error.html", &htmlErr{
			ErrorCode:    http.StatusBadRequest,
			ErrorMessage: "failed to parse form",
		})
		return
	}

	var shortNamePtr *string
	shortName := strings.TrimSpace(r.Form.Get("short_name"))
	if shortName != "" {
		shortNamePtr = &shortName
	}

	if err := api.db.SetShortCharName(r.Context(), characterID, shortNamePtr); err != nil {
		_ = html.ExecuteTemplate(w, "error.html", &htmlErr{
			ErrorCode:    http.StatusInternalServerError,
			ErrorMessage: err.Error(),
		})
		return
	}

	_, _ = w.Write([]byte("Success"))
}

type characterPage struct {
	CharacterID     uuid.UUID
	Card            *db.Card
	MessageExamples *msgExamples
}

func (api *API) character(r *http.Request) template.HTML {
	user := ctxstore.GetUser(r.Context())
	if user == nil {
		return getHtml("error.html", &htmlErr{
			ErrorCode:    http.StatusUnauthorized,
			ErrorMessage: "not logged in",
		})
	}

	characterIDStr := chi.URLParam(r, "character_id")

	var card *db.Card
	msgExamples := &msgExamples{
		ID: 0,
	}

	var characterID uuid.UUID = uuid.Nil

	if characterIDStr == "new" {
		msgExamples.MessageExamples = append(msgExamples.MessageExamples, msgExample{
			ID: 0,
		})
	} else {
		var err error
		characterID, err = uuid.Parse(characterIDStr)
		if err != nil {
			return getHtml("error.html", &htmlErr{
				ErrorCode:    http.StatusBadRequest,
				ErrorMessage: "character_id is not a valid uuid",
			})
		}

		card, err = api.db.GetCharCardByID(r.Context(), user.ID, characterID)
		if err != nil {
			return getHtml("error.html", &htmlErr{
				ErrorCode:    http.StatusInternalServerError,
				ErrorMessage: err.Error(),
			})
		}

		for i, messageExample := range card.Data.MessageExamples {
			msgExamples.MessageExamples = append(msgExamples.MessageExamples, msgExample{
				ID:       i,
				Request:  messageExample.Request,
				Response: messageExample.Response,
			})
			msgExamples.ID = i
		}
	}

	return getHtml("character.html", &characterPage{
		CharacterID:     characterID,
		Card:            card,
		MessageExamples: msgExamples,
	})
}

type entry struct {
	data string
	id   int
}

const requestPrefix = "request_"
const responsePrefix = "response_"

func parseEntry(idStr, val string) (*entry, error) {
	id, err := strconv.Atoi(idStr)
	if err != nil {
		return nil, fmt.Errorf("strconv.Atoi(%s): %w", idStr, err)
	}

	return &entry{
		data: val,
		id:   id,
	}, nil
}

func formToCard(form url.Values) (*db.Card, error) {
	card := &db.Card{
		Data: &db.CardData{},
	}

	if form.Has("public") {
		card.Public = true
	}

	card.Name = form.Get("char_name")
	card.Description = form.Get("char_description")

	card.Data.Name = form.Get("name")
	card.Data.Description = form.Get("description")
	card.Data.Personality = form.Get("personality")

	requests := make([]entry, 0, len(form))
	responses := make([]entry, 0, len(form))
	for key, val := range form {
		if strings.HasPrefix(key, requestPrefix) && len(key) > len(requestPrefix) && len(val) > 0 {
			if entry, err := parseEntry(key[len(requestPrefix):], val[0]); err != nil {
				return nil, fmt.Errorf("parseEntry(%s, %s): %w", key, val[0], err)
			} else {
				requests = append(requests, *entry)
			}
		}
		if strings.HasPrefix(key, responsePrefix) && len(key) > len(responsePrefix) && len(val) > 0 {
			if entry, err := parseEntry(key[len(responsePrefix):], val[0]); err != nil {
				return nil, fmt.Errorf("parseEntry(%s, %s): %w", key, val[0], err)
			} else {
				responses = append(responses, *entry)
			}
		}
	}
	if len(requests) != len(responses) {
		return nil, fmt.Errorf("requests and responses must have the same length")
	}
	sort.Slice(requests, func(i, j int) bool {
		return requests[i].id < requests[j].id
	})
	sort.Slice(responses, func(i, j int) bool {
		return responses[i].id < responses[j].id
	})
	messageExamples := make([]db.MessageExample, 0, len(requests))
	for i := 0; i < len(requests); i++ {
		messageExamples = append(messageExamples, db.MessageExample{
			Request:  requests[i].data,
			Response: responses[i].data,
		})
	}

	card.Data.MessageExamples = messageExamples

	card.Data.FirstMessage = form.Get("first_message")

	return card, nil
}

func (api *API) extractVoiceRef(r *http.Request) ([]byte, error) {
	file, _, err := r.FormFile("voice_ref")
	if err != nil {
		return nil, fmt.Errorf("r.FormFile(): %w", err)
	}
	defer file.Close()

	voiceRef, err := io.ReadAll(file)
	if err != nil {
		return nil, fmt.Errorf("io.ReadAll(): %w", err)
	}

	return voiceRef, nil
}

func (api *API) extractImage(r *http.Request) ([]byte, error) {
	file, _, err := r.FormFile("image")
	if err != nil {
		return nil, fmt.Errorf("r.FormFile(): %w", err)
	}

	image, err := io.ReadAll(file)
	if err != nil {
		return nil, fmt.Errorf("io.ReadAll(): %w", err)
	}

	return image, nil
}

func (api *API) updateCharacter(user *db.User, card *db.Card, w http.ResponseWriter, r *http.Request) {
	var voiceRef []byte
	if _, ok := r.MultipartForm.File["voice_ref"]; !ok {
		oldCard, err := api.db.GetCharCardByID(r.Context(), user.ID, card.ID)
		if err != nil {
			_ = html.ExecuteTemplate(w, "error.html", &htmlErr{
				ErrorCode:    http.StatusInternalServerError,
				ErrorMessage: err.Error(),
			})
			return
		}

		voiceRef = oldCard.Data.VoiceReference
	} else {
		var err error
		voiceRef, err = api.extractVoiceRef(r)
		if err != nil {
			_ = html.ExecuteTemplate(w, "error.html", &htmlErr{
				ErrorCode:    http.StatusInternalServerError,
				ErrorMessage: err.Error(),
			})
			return
		}
	}
	card.Data.VoiceReference = voiceRef

	var image []byte
	if _, ok := r.MultipartForm.File["image"]; !ok {
		oldCard, err := api.db.GetCharCardByID(r.Context(), user.ID, card.ID)
		if err != nil {
			_ = html.ExecuteTemplate(w, "error.html", &htmlErr{
				ErrorCode:    http.StatusInternalServerError,
				ErrorMessage: err.Error(),
			})
			return
		}

		image = oldCard.Data.Image
	} else {
		var err error
		image, err = api.extractImage(r)
		if err != nil {
			_ = html.ExecuteTemplate(w, "error.html", &htmlErr{
				ErrorCode:    http.StatusInternalServerError,
				ErrorMessage: err.Error(),
			})
			return
		}
	}
	card.Data.Image = image

	if err := api.db.UpdateCharCard(r.Context(), user.ID, card); err != nil {
		_ = html.ExecuteTemplate(w, "error.html", &htmlErr{
			ErrorCode:    http.StatusInternalServerError,
			ErrorMessage: "UpdateCharCard: " + err.Error(),
		})
		return
	}

	// w.Header().Add("hx-redirect", "/characters/"+card.ID.String())
	_, _ = w.Write([]byte("Success"))
}

func (api *API) insertCharacter(user *db.User, card *db.Card, w http.ResponseWriter, r *http.Request) {
	voiceRef, err := api.extractVoiceRef(r)
	if err != nil {
		_ = html.ExecuteTemplate(w, "error.html", &htmlErr{
			ErrorCode:    http.StatusInternalServerError,
			ErrorMessage: "extractVoiceRef: " + err.Error(),
		})
		return
	}

	if len(voiceRef) == 0 {
		_ = html.ExecuteTemplate(w, "error.html", &htmlErr{
			ErrorCode:    http.StatusInternalServerError,
			ErrorMessage: "No Voice Reference Provided",
		})
		return
	}

	image, err := api.extractImage(r)
	if err != nil {
		_ = html.ExecuteTemplate(w, "error.html", &htmlErr{
			ErrorCode:    http.StatusInternalServerError,
			ErrorMessage: "extractImage: " + err.Error(),
		})
		return
	}

	if len(image) == 0 {
		_ = html.ExecuteTemplate(w, "error.html", &htmlErr{
			ErrorCode:    http.StatusInternalServerError,
			ErrorMessage: "No Image Provided",
		})
		return
	}

	card.OwnerUserID = user.ID
	card.Data.VoiceReference = voiceRef
	card.Data.Image = image

	cardID, err := api.db.InsertCharCard(r.Context(), card)
	if err != nil {
		_ = html.ExecuteTemplate(w, "error.html", &htmlErr{
			ErrorCode:    http.StatusInternalServerError,
			ErrorMessage: "InsertCharCard: " + err.Error(),
		})
		return
	}

	w.Header().Add("hx-redirect", "/characters/"+cardID.String())
	_, _ = w.Write([]byte("Success"))
	// w.WriteHeader(http.StatusOK)
}

func (api *API) upsertCharacter(w http.ResponseWriter, r *http.Request) {
	characterIDStr := chi.URLParam(r, "character_id")

	user := ctxstore.GetUser(r.Context())
	if user == nil {
		_ = html.ExecuteTemplate(w, "error.html", &htmlErr{
			ErrorCode:    http.StatusUnauthorized,
			ErrorMessage: "not authorized",
		})
		return
	}

	if err := r.ParseMultipartForm(20 * 1024 * 1024); err != nil {
		_ = html.ExecuteTemplate(w, "error.html", &htmlErr{
			ErrorCode:    http.StatusInternalServerError,
			ErrorMessage: "r.ParseMultipartForm(): " + err.Error(),
		})
		return
	}

	_ = r.ParseForm()

	card, err := formToCard(r.Form)
	if err != nil {
		_ = html.ExecuteTemplate(w, "error.html", &htmlErr{
			ErrorCode:    http.StatusInternalServerError,
			ErrorMessage: "formToCard: " + err.Error(),
		})
		return
	}

	characterID, err := uuid.Parse(characterIDStr)
	if err != nil {
		_ = html.ExecuteTemplate(w, "error.html", &htmlErr{
			ErrorCode:    http.StatusInternalServerError,
			ErrorMessage: "{character_id} is not valid uuid: " + err.Error(),
		})
		return
	}

	card.ID = characterID
	card.OwnerUserID = user.ID

	if characterID == uuid.Nil {
		api.insertCharacter(user, card, w, r)
	} else {
		api.updateCharacter(user, card, w, r)
	}
}

type msgExample struct {
	ID       int
	Request  string
	Response string
}

type msgExamples struct {
	ID              int
	MessageExamples []msgExample
}

func (api *API) newMessageExample(r *http.Request) template.HTML {
	idStr := chi.URLParam(r, "id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		return getHtml("error.html", &htmlErr{
			ErrorCode:    http.StatusBadRequest,
			ErrorMessage: "",
		})
	}

	return getHtml("message_example.html", &msgExamples{
		MessageExamples: []msgExample{
			{
				ID: id + 1,
			},
		},
		ID: id + 1,
	})
}

func (api *API) charImage(w http.ResponseWriter, r *http.Request) {
	// user := ctxstore.GetUser(r.Context())
	// if user == nil {
	// 	_ = html.ExecuteTemplate(w, "error.html", &htmlErr{
	// 		ErrorCode:    http.StatusUnauthorized,
	// 		ErrorMessage: "not authorized",
	// 	})
	// 	return
	// } // for use in obs

	characterIDStr := chi.URLParam(r, "character_id")
	characterID, err := uuid.Parse(characterIDStr)
	if err != nil {
		_ = html.ExecuteTemplate(w, "error.html", &htmlErr{
			ErrorCode:    http.StatusBadRequest,
			ErrorMessage: "{character_id} is not valid uuid: " + err.Error(),
		})
		return
	}

	charImage, err := api.db.GetCharImage(r.Context(), characterID)
	if err != nil {
		_ = html.ExecuteTemplate(w, "error.html", &htmlErr{
			ErrorCode:    http.StatusInternalServerError,
			ErrorMessage: "GetCharImage: " + err.Error(),
		})
		return
	}

	// Encourage caching for repeated visits
	w.Header().Set("Cache-Control", "public, max-age=3600")

	if len(charImage) == 0 {
		img, err := staticFS.ReadFile("static/doctorWTF.png")
		if err != nil {
			_ = html.ExecuteTemplate(w, "error.html", &htmlErr{
				ErrorCode:    http.StatusInternalServerError,
				ErrorMessage: "staticFS.ReadFile: " + err.Error(),
			})
			return
		}

		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(img)
		return
	}

	_, _ = w.Write(charImage)
}

type voicesListPage struct {
	Items []voiceItem
}

type voiceItem struct {
	ID   uuid.UUID
	Name string
}

// voicesPublic renders a public page with all public short-named cards and their images
func (api *API) voicesPublic(r *http.Request) template.HTML {
	items, err := api.db.GetPublicShortNamedCards(r.Context())
	if err != nil {
		return getHtml("error.html", &htmlErr{
			ErrorCode:    http.StatusInternalServerError,
			ErrorMessage: err.Error(),
		})
	}

	list := make([]voiceItem, 0, len(items))
	for _, it := range items {
		list = append(list, voiceItem{ID: it.ID, Name: it.ShortCharName})
	}

	return getHtml("voices.html", &voicesListPage{Items: list})
}

func (api *API) universalTTSReward(w http.ResponseWriter, r *http.Request) {
	user := ctxstore.GetUser(r.Context())
	if user == nil {
		_ = html.ExecuteTemplate(w, "error.html", &htmlErr{
			ErrorCode:    http.StatusUnauthorized,
			ErrorMessage: "not authorized",
		})
		return
	}

	prompt := "Voices: " + r.Host + "/voices"

	err := api.createReward(r.Context(), w, user, nil, "", db.TwitchRewardUniversalTTS, prompt)
	if err != nil {
		return
	}
}

func (api *API) agenticReward(w http.ResponseWriter, r *http.Request) {
	user := ctxstore.GetUser(r.Context())
	if user == nil {
		_ = html.ExecuteTemplate(w, "error.html", &htmlErr{
			ErrorCode:    http.StatusUnauthorized,
			ErrorMessage: "not authorized",
		})
		return
	}

	err := api.createReward(r.Context(), w, user, nil, "", db.TwitchRewardAgentic, "")
	if err != nil {
		return
	}
}

type tryPageData struct {
	CharacterID   uuid.UUID
	CharacterName string
	AgenticMode   bool
}

// tryCharacter serves the try page for testing a character
func (api *API) tryCharacter(r *http.Request) template.HTML {
	user := ctxstore.GetUser(r.Context())
	if user == nil {
		return getHtml("error.html", &htmlErr{
			ErrorCode:    http.StatusUnauthorized,
			ErrorMessage: "not logged in",
		})
	}

	characterIDStr := chi.URLParam(r, "character_id")
	characterID, err := uuid.Parse(characterIDStr)
	if err != nil {
		return getHtml("error.html", &htmlErr{
			ErrorCode:    http.StatusBadRequest,
			ErrorMessage: "character_id is not a valid uuid",
		})
	}

	card, err := api.db.GetCharCardByID(r.Context(), user.ID, characterID)
	if err != nil {
		return getHtml("error.html", &htmlErr{
			ErrorCode:    http.StatusInternalServerError,
			ErrorMessage: err.Error(),
		})
	}

	// Check if user can access this character (owns it or it's public)
	if card.OwnerUserID != user.ID && !card.Public {
		return getHtml("error.html", &htmlErr{
			ErrorCode:    http.StatusForbidden,
			ErrorMessage: "You don't have access to this character",
		})
	}

	return getHtml("try_character.html", &tryPageData{
		CharacterID:   characterID,
		CharacterName: card.Name,
		AgenticMode:   false,
	})
}

type tryAction struct {
	Action string `json:"action"`
	Text   string `json:"text"`
}

type tryJob struct {
	Action string
	Input  processor.InteractionInput
}

type TryHandler interface {
	Handle(ctx context.Context, input processor.InteractionInput, writer conns.EventWriter) error
}

func (api *API) readTryCommands(ctx context.Context, wsClient *ws.Client, state *processor.ProcessorState, eventWriter conns.EventWriter, card *db.Card, dbUser *db.User, userSettings *db.UserSettings) <-chan tryJob {
	jobCh := make(chan tryJob)

	go func() {
		defer close(jobCh)

		for {
			msg, err := wsClient.Read()
			if err != nil {
				if !errors.Is(err, ws.ErrClosed) {
					api.logger.Error("failed to read from ws", "err", err)
				}
				return
			}

			var action *tryAction
			err = json.Unmarshal(msg.Message, &action)
			if err != nil {
				api.logger.Error("failed to unmarshal message from ws", "err", err)
				continue
			}

			if action.Action == "stop" {
				currentMsgID := state.GetCurrent()
				if currentMsgID != uuid.Nil {
					state.AddSkipped(currentMsgID)
					eventWriter(&conns.DataEvent{
						EventType: conns.EventTypeSkip,
						EventData: []byte(currentMsgID.String()),
					})
					api.logger.Info("stopped current message", "msg_id", currentMsgID)
				}
				continue
			}

			if action.Text == "" {
				api.logger.Warn("empty text in action")
				continue
			}

			api.logger.Info("processing try action", "action", action.Action, "text", action.Text)

			msgID := uuid.New()

			input := processor.InteractionInput{
				Requester:    dbUser.TwitchLogin,
				Broadcaster:  dbUser,
				Message:      action.Text,
				Character:    card,
				UserSettings: userSettings,
				MsgID:        msgID.String(),
				State:        state,
			}

			select {
			case jobCh <- tryJob{Action: action.Action, Input: input}:
			case <-ctx.Done():
				return
			}
		}
	}()

	return jobCh
}

func (api *API) serveTryWS(w http.ResponseWriter, r *http.Request, card *db.Card, handlers map[string]TryHandler) {
	user := ctxstore.GetUser(r.Context())
	logger := api.logger.With("user", user.TwitchLogin, "character_id", card.ID)

	logger.Info("received websocket connection request")

	wsConn, err := ws.Upgrader.Upgrade(w, r, nil)
	if err != nil {
		logger.Error("failed to upgrade to websocket connection", "err", err)
		return
	}

	wsClient, done := ws.NewWsClient(wsConn)
	defer func() {
		logger.Info("closing websocket connection")
		wsClient.Close()
	}()

	logger.Info("websocket connection established")

	userSettings, err := api.db.GetUserSettings(r.Context(), user.ID)
	if err != nil {
		logger.Warn("failed to get user settings, using defaults", "err", err)
		userSettings = &db.UserSettings{}
	}

	eventCh := make(chan *conns.DataEvent, 100)
	var wg sync.WaitGroup
	defer func() {
		wg.Wait()
		close(eventCh)
	}()

	eventWriter := conns.EventWriter(func(event *conns.DataEvent) bool {
		select {
		case eventCh <- event:
			return true
		default:
			logger.Warn("event channel full, dropping event")
			return false
		}
	})

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	state := processor.NewProcessorState()

	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(3 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := sendData(wsClient, conns.EventTypePing.String(), []byte("ping")); err != nil {
					logger.Error("failed to send ping", "err", err)
					return
				}
			case event := <-eventCh:
				if err := sendData(wsClient, event.EventType.String(), event.EventData); err != nil {
					logger.Error("failed to write to ws", "err", err)
					return
				}
			case <-done:
				return
			}
		}
	}()

	jobCh := api.readTryCommands(ctx, wsClient, state, eventWriter, card, user, userSettings)

	for job := range jobCh {
		if msgID, err := uuid.Parse(job.Input.MsgID); err == nil {
			state.SetCurrent(msgID)
		}

		handler, ok := handlers[job.Action]
		if !ok {
			logger.Error("unknown action", "action", job.Action)
			state.SetCurrent(uuid.Nil)
			continue
		}

		if err := handler.Handle(ctx, job.Input, eventWriter); err != nil {
			logger.Error("handler failed", "err", err)
			eventWriter(&conns.DataEvent{
				EventType: conns.EventTypeText,
				EventData: []byte("Error: " + err.Error()),
			})
		}

		state.SetCurrent(uuid.Nil)
	}
}

func (api *API) tryCharacterWS(w http.ResponseWriter, r *http.Request) {
	user := ctxstore.GetUser(r.Context())
	if user == nil {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("not authorized"))
		return
	}

	characterIDStr := chi.URLParam(r, "character_id")
	characterID, err := uuid.Parse(characterIDStr)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("invalid character_id"))
		return
	}

	logger := api.logger.With("user", user.TwitchLogin, "character_id", characterID)

	card, err := api.db.GetCharCardByID(r.Context(), user.ID, characterID)
	if err != nil {
		logger.Error("failed to get character card", "err", err)
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("character not found"))
		return
	}

	if card.OwnerUserID != user.ID && !card.Public {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("access denied"))
		return
	}

	api.serveTryWS(w, r, card, map[string]TryHandler{
		"tts": api.ttsHandler,
		"ai":  api.aiHandler,
	})
}

func (api *API) tryAgentic(r *http.Request) template.HTML {
	user := ctxstore.GetUser(r.Context())
	if user == nil {
		return getHtml("error.html", &htmlErr{
			ErrorCode:    http.StatusUnauthorized,
			ErrorMessage: "not logged in",
		})
	}

	return getHtml("try_character.html", &tryPageData{
		CharacterName: "Agentic Flow",
		AgenticMode:   true,
	})
}

func (api *API) tryAgenticWS(w http.ResponseWriter, r *http.Request) {
	user := ctxstore.GetUser(r.Context())
	if user == nil {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("not authorized"))
		return
	}

	dummyCard := &db.Card{
		OwnerUserID: user.ID,
	}

	api.serveTryWS(w, r, dummyCard, map[string]TryHandler{
		"agentic": api.agenticHandler,
	})
}
