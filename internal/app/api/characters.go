package api

import (
	"app/db"
	"app/pkg/ctxstore"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
)

type charsElem struct {
	Characters []*charElem
}

type charElem struct {
	Card    *db.Card
	CanEdit bool
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
		SortBy:     db.SortByRedeems,
	})
	if err != nil {
		return getHtml("error.html", &htmlErr{
			ErrorCode:    http.StatusInternalServerError,
			ErrorMessage: "GetCharCards: " + err.Error(),
		})
	}

	chars := make([]*charElem, 0, len(charCards))
	for _, charCard := range charCards {
		chars = append(chars, &charElem{
			Card:    charCard,
			CanEdit: user.ID == charCard.OwnerUserID,
		})
	}

	return getHtml("characters.html", &charsElem{
		Characters: chars,
	})
}

type characterPage struct {
	CharacterID     int
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
	characterID, err := strconv.Atoi(characterIDStr)
	if err != nil {
		return getHtml("error.html", &htmlErr{
			ErrorCode:    http.StatusBadRequest,
			ErrorMessage: "character_id is not a number",
		})
	}

	var card *db.Card
	msgExamples := &msgExamples{
		ID: 0,
	}
	if characterID != 0 {
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
	} else {
		msgExamples.MessageExamples = append(msgExamples.MessageExamples, msgExample{
			ID: 0,
		})
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

	card.CharName = form.Get("char_name")
	card.CharDescription = form.Get("char_description")

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

func (api *API) upsertCharacter(w http.ResponseWriter, r *http.Request) {
	characterIDStr := chi.URLParam(r, "character_id")
	characterID, err := strconv.Atoi(characterIDStr)
	if err != nil {
		_ = html.ExecuteTemplate(w, "error.html", &htmlErr{
			ErrorCode:    http.StatusInternalServerError,
			ErrorMessage: "{character_id} is not integer: " + err.Error(),
		})
	}

	user := ctxstore.GetUser(r.Context())
	if user == nil {
		_ = html.ExecuteTemplate(w, "error.html", &htmlErr{
			ErrorCode:    http.StatusUnauthorized,
			ErrorMessage: "not authorized",
		})
	}

	if err := r.ParseForm(); err != nil {
		_ = html.ExecuteTemplate(w, "error.html", &htmlErr{
			ErrorCode:    http.StatusInternalServerError,
			ErrorMessage: "r.ParseForm(): " + err.Error(),
		})
		return
	}

	card, err := formToCard(r.Form)
	if err != nil {
		_ = html.ExecuteTemplate(w, "error.html", &htmlErr{
			ErrorCode:    http.StatusInternalServerError,
			ErrorMessage: "formToCard: " + err.Error(),
		})
		return
	}

	card.ID = characterID
	card.OwnerUserID = user.ID

	if card.ID == 0 {
		cardID, err := api.db.InsertCharCard(r.Context(), card)
		if err != nil {
			_ = html.ExecuteTemplate(w, "error.html", &htmlErr{
				ErrorCode:    http.StatusInternalServerError,
				ErrorMessage: "InsertCharCard: " + err.Error(),
			})
			return
		}

		w.Header().Add("hx-redirect", "/characters/"+strconv.Itoa(cardID))
		w.WriteHeader(http.StatusOK)

		return
	}

	if err = api.db.UpdateCharCard(r.Context(), user.ID, card); err != nil {
		_ = html.ExecuteTemplate(w, "error.html", &htmlErr{
			ErrorCode:    http.StatusInternalServerError,
			ErrorMessage: "UpdateCharCard: " + err.Error(),
		})
		return
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
	user := ctxstore.GetUser(r.Context())
	if user == nil {
		_ = html.ExecuteTemplate(w, "error.html", &htmlErr{
			ErrorCode:    http.StatusUnauthorized,
			ErrorMessage: "not authorized",
		})
		return
	}

	characterIDStr := chi.URLParam(r, "character_id")
	characterID, err := strconv.Atoi(characterIDStr)
	if err != nil {
		_ = html.ExecuteTemplate(w, "error.html", &htmlErr{
			ErrorCode:    http.StatusBadRequest,
			ErrorMessage: "{character_id} is not integer: " + err.Error(),
		})
		return
	}

	card, err := api.db.GetCharCardByID(r.Context(), user.ID, characterID)
	if err != nil {
		_ = html.ExecuteTemplate(w, "error.html", &htmlErr{
			ErrorCode:    http.StatusInternalServerError,
			ErrorMessage: "GetCharCardByID: " + err.Error(),
		})
		return
	}

	if len(card.Data.Image) == 0 {
		img, err := staticFS.ReadFile("static/doctorWTF.png")
		if err != nil {
			_ = html.ExecuteTemplate(w, "error.html", &htmlErr{
				ErrorCode:    http.StatusInternalServerError,
				ErrorMessage: "staticFS.ReadFile: " + err.Error(),
			})
			return
		}

		_, _ = w.Write(img)
		return
	}

	_, _ = w.Write(card.Data.Image)
}
