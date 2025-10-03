package api

import (
	"app/db"
	"app/pkg/ctxstore"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

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
