package api

import (
	"app/db"
	"app/internal/app/conns"
	"app/internal/app/processor"
	"app/pkg/ai"
	"app/pkg/charassist"
	"app/pkg/ctxstore"
	"app/pkg/s3client"
	"app/pkg/tools"
	"app/pkg/ws"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

const newCardWindow = 14 * 24 * time.Hour

func buildSearchText(parts ...string) string {
	var clean []string
	for _, p := range parts {
		if fields := strings.Fields(p); len(fields) > 0 {
			clean = append(clean, strings.ToLower(strings.Join(fields, " ")))
		}
	}
	return strings.Join(clean, " ")
}

func matchesQuery(haystack string, terms []string) bool {
	for _, term := range terms {
		if !strings.Contains(haystack, term) {
			return false
		}
	}
	return true
}

func queryTerms(q string) []string {
	return strings.Fields(strings.ToLower(strings.TrimSpace(q)))
}

type charsElem struct {
	Characters    []*charElem
	IsAdmin       bool
	Query         string
	Sort          string
	Scope         string
	Filtered      bool
	SetQueryOOB   bool
	TotalCount    int
	MineCount     int
	EnabledCount  int
	DisabledCount int
	NewCount      int
	NoVoiceCount  int
}

type charElem struct {
	Card *db.Card

	CanEdit bool
	IsAdmin bool

	TTSRewardCreated bool
	AIRewardCreated  bool

	Author       string
	Disabled     bool
	DefaultVoice bool
	IsNew        bool
	NeedsVoice   bool
	TotalRedeems int
	SearchText   string
}

func normalizeCharsSort(raw string) string {
	switch raw {
	case "trending", "popular", "ai", "tts", "az":
		return raw
	default:
		return "newest"
	}
}

func normalizeCharsScope(raw string, isAdmin bool) string {
	switch raw {
	case "mine", "enabled", "disabled", "new":
		return raw
	case "no-voice":
		if isAdmin {
			return raw
		}
	}
	return "all"
}

func filterAndSortChars(all []*charElem, query, sortMode, scope string) []*charElem {
	terms := queryTerms(query)
	out := make([]*charElem, 0, len(all))
	for _, c := range all {
		switch scope {
		case "mine":
			if !c.CanEdit {
				continue
			}
		case "enabled":
			if c.Disabled {
				continue
			}
		case "disabled":
			if !c.Disabled {
				continue
			}
		case "new":
			if !c.IsNew {
				continue
			}
		case "no-voice":
			if !c.NeedsVoice {
				continue
			}
		}
		if len(terms) > 0 && !matchesQuery(c.SearchText, terms) {
			continue
		}
		out = append(out, c)
	}

	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		switch sortMode {
		case "trending":
			if a.Card.TrendingScore != b.Card.TrendingScore {
				return a.Card.TrendingScore > b.Card.TrendingScore
			}
			if a.TotalRedeems != b.TotalRedeems {
				return a.TotalRedeems > b.TotalRedeems
			}
			return a.Card.CreatedAt.After(b.Card.CreatedAt)
		case "popular":
			if a.TotalRedeems != b.TotalRedeems {
				return a.TotalRedeems > b.TotalRedeems
			}
			return a.Card.CreatedAt.After(b.Card.CreatedAt)
		case "ai":
			if a.Card.Redeems != b.Card.Redeems {
				return a.Card.Redeems > b.Card.Redeems
			}
			return a.TotalRedeems > b.TotalRedeems
		case "tts":
			if a.Card.TTSRedeems != b.Card.TTSRedeems {
				return a.Card.TTSRedeems > b.Card.TTSRedeems
			}
			return a.TotalRedeems > b.TotalRedeems
		case "az":
			return strings.ToLower(a.Card.Name) < strings.ToLower(b.Card.Name)
		default:
			return a.Card.CreatedAt.After(b.Card.CreatedAt)
		}
	})

	return out
}

func (api *API) loadCharactersPage(r *http.Request, user *db.User) (*charsElem, error) {
	charCards, err := api.db.GetCharCards(r.Context(), user.ID, db.GetChatCardsParams{
		ShowPublic: true,
		SortBy:     db.SortByNewest,
	})
	if err != nil {
		return nil, fmt.Errorf("GetCharCards: %w", err)
	}

	settings, err := api.db.GetUserSettings(r.Context(), user.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to get user settings: %w", err)
	}

	isAdmin := false
	if perms, err := api.db.GetUserPermissions(r.Context(), user.ID, db.PermissionStatusGranted); err == nil {
		for _, p := range perms {
			if p == db.PermissionAdmin {
				isAdmin = true
				break
			}
		}
	}

	q := r.URL.Query()
	query := strings.TrimSpace(q.Get("q"))
	sortMode := normalizeCharsSort(q.Get("sort"))
	scope := normalizeCharsScope(q.Get("scope"), isAdmin)

	now := time.Now()
	page := &charsElem{
		IsAdmin:     isAdmin,
		Query:       query,
		Sort:        sortMode,
		Scope:       scope,
		Filtered:    query != "" || sortMode != "newest" || scope != "all",
		SetQueryOOB: q.Get("set_q") == "1",
		TotalCount:  len(charCards),
	}

	all := make([]*charElem, 0, len(charCards))
	for _, charCard := range charCards {
		canEdit := user.ID == charCard.OwnerUserID
		disabled := settings.CardDisabled(charCard.ID)
		isNew := !charCard.CreatedAt.IsZero() && now.Sub(charCard.CreatedAt) <= newCardWindow
		needsVoice := charCard.Public && strings.TrimSpace(charCard.ShortCharName.String) == ""

		if canEdit {
			page.MineCount++
		}
		if disabled {
			page.DisabledCount++
		} else {
			page.EnabledCount++
		}
		if isNew {
			page.NewCount++
		}
		if needsVoice {
			page.NoVoiceCount++
		}

		all = append(all, &charElem{
			Card:             charCard,
			CanEdit:          canEdit,
			IsAdmin:          isAdmin,
			TTSRewardCreated: false,
			Author:           charCard.OwnerTwitchLogin,
			Disabled:         disabled,
			DefaultVoice:     strings.EqualFold(charCard.ShortCharName.String, processor.DefaultUniversalVoice),
			IsNew:            isNew,
			NeedsVoice:       needsVoice,
			TotalRedeems:     charCard.Redeems + charCard.TTSRedeems,
			SearchText:       buildSearchText(charCard.Name, charCard.ShortCharName.String, charCard.OwnerTwitchLogin, charCard.Description),
		})
	}

	page.Characters = filterAndSortChars(all, query, sortMode, scope)
	return page, nil
}

func (api *API) characters(r *http.Request) template.HTML {
	user := ctxstore.GetUser(r.Context())
	if user == nil {
		return getHtml("error.html", &htmlErr{
			ErrorCode:    http.StatusInternalServerError,
			ErrorMessage: "unauthorized",
		})
	}

	page, err := api.loadCharactersPage(r, user)
	if err != nil {
		return getHtml("error.html", &htmlErr{
			ErrorCode:    http.StatusInternalServerError,
			ErrorMessage: err.Error(),
		})
	}

	return getHtml("characters.html", page)
}

func (api *API) charactersList(w http.ResponseWriter, r *http.Request) {
	user := ctxstore.GetUser(r.Context())
	if user == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	page, err := api.loadCharactersPage(r, user)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	_ = html.ExecuteTemplate(w, "chars-results", page)
}

func (api *API) setCharacterEnabled(w http.ResponseWriter, r *http.Request) {
	user := ctxstore.GetUser(r.Context())
	if user == nil {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	characterID, err := uuid.Parse(chi.URLParam(r, "character_id"))
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("character_id is not a valid uuid"))
		return
	}

	if err := r.ParseForm(); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("failed to parse form"))
		return
	}

	if err := api.db.SetCardDisabled(r.Context(), user.ID, characterID, r.Form.Get("enabled") == ""); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(err.Error()))
		return
	}

	if r.Header.Get("HX-Request") == "true" {
		if cards, err := api.db.GetCharCards(r.Context(), user.ID, db.GetChatCardsParams{ShowPublic: true}); err == nil {
			if settings, err := api.db.GetUserSettings(r.Context(), user.ID); err == nil {
				enabled, disabled := 0, 0
				for _, c := range cards {
					if settings.CardDisabled(c.ID) {
						disabled++
					} else {
						enabled++
					}
				}
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				_, _ = fmt.Fprintf(w, `<span id="chars-count-enabled" hx-swap-oob="innerHTML">%d</span><span id="chars-count-disabled" hx-swap-oob="innerHTML">%d</span>`, enabled, disabled)
				return
			}
		}
	}

	w.WriteHeader(http.StatusNoContent)
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
	VoiceRefHash    fileHash
	ImageHash       fileHash
	Assist          *charAssistPanelData
}

func (p *characterPage) Field(name string) charFieldData {
	if p == nil || p.Card == nil {
		return charFieldData{}
	}
	switch name {
	case "char_name":
		return charFieldData{Value: p.Card.Name}
	case "char_description":
		return charFieldData{Value: p.Card.Description}
	case "name":
		if p.Card.Data != nil {
			return charFieldData{Value: p.Card.Data.Name}
		}
	case "description":
		if p.Card.Data != nil {
			return charFieldData{Value: p.Card.Data.Description}
		}
	case "personality":
		if p.Card.Data != nil {
			return charFieldData{Value: p.Card.Data.Personality}
		}
	case "first_message":
		if p.Card.Data != nil {
			return charFieldData{Value: p.Card.Data.FirstMessage}
		}
	}
	return charFieldData{}
}

func (p *characterPage) ExamplesContainer() charExamplesData {
	return charExamplesData{Examples: p.MessageExamples}
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

	page := &characterPage{
		CharacterID:     characterID,
		Card:            card,
		MessageExamples: msgExamples,
		VoiceRefHash:    newFileHash(characterSaveStatusID, characterSaveSkip, "voice_ref", nil),
		ImageHash:       newFileHash(characterSaveStatusID, characterSaveSkip, "image", nil),
	}
	if api.charAssist != nil {
		hasSearch := api.charAssist.HasSearch()
		draft := cardToDraft(card)
		page.Assist = &charAssistPanelData{
			SessionID: uuid.New().String(),
			HasSearch: hasSearch,
			SearchOn:  hasSearch,
			Tips:      charassist.ContextualTips(draft, hasSearch),
			AsyncTips: draft.HasContent(),
		}
	}
	if card != nil {
		page.VoiceRefHash.Hash = hashBytes(card.Data.VoiceReference)
		page.ImageHash.Hash = hashBytes(card.Data.Image)
	}

	return getHtml("character.html", page)
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
	defer file.Close()

	image, err := io.ReadAll(file)
	if err != nil {
		return nil, fmt.Errorf("io.ReadAll(): %w", err)
	}

	return image, nil
}

const (
	characterSaveStatusID = "character_save_result"
	// the form's file inputs
	characterSaveSkip = "voice_ref,image"
)

func characterSaveError(w http.ResponseWriter, r *http.Request, message string) {
	writeSaveError(w, r, characterSaveStatusID, characterSaveSkip, http.StatusUnprocessableEntity, message)
}

func (api *API) updateCharacter(user *db.User, card *db.Card, w http.ResponseWriter, r *http.Request) {
	var voiceRef []byte
	if _, ok := r.MultipartForm.File["voice_ref"]; !ok {
		oldCard, err := api.db.GetCharCardByID(r.Context(), user.ID, card.ID)
		if err != nil {
			characterSaveError(w, r, err.Error())
			return
		}

		voiceRef = oldCard.Data.VoiceReference
	} else {
		var err error
		voiceRef, err = api.extractVoiceRef(r)
		if err != nil {
			characterSaveError(w, r, err.Error())
			return
		}
	}
	card.Data.VoiceReference = voiceRef

	var image []byte
	if _, ok := r.MultipartForm.File["image"]; !ok {
		oldCard, err := api.db.GetCharCardByID(r.Context(), user.ID, card.ID)
		if err != nil {
			characterSaveError(w, r, err.Error())
			return
		}

		image = oldCard.Data.Image
	} else {
		var err error
		image, err = api.extractImage(r)
		if err != nil {
			characterSaveError(w, r, err.Error())
			return
		}
	}
	card.Data.Image = image

	if err := api.db.UpdateCharCard(r.Context(), user.ID, card); err != nil {
		characterSaveError(w, r, "UpdateCharCard: "+err.Error())
		return
	}

	api.imageCache.Invalidate(card.ID)

	writeSaved(w, r, characterSaveStatusID, characterSaveSkip)
}

func (api *API) insertCharacter(user *db.User, card *db.Card, w http.ResponseWriter, r *http.Request) {
	voiceRef, err := api.extractVoiceRef(r)
	if err != nil || len(voiceRef) == 0 {
		characterSaveError(w, r, "voice reference is required")
		return
	}

	image, err := api.extractImage(r)
	if err != nil || len(image) == 0 {
		characterSaveError(w, r, "image is required")
		return
	}

	card.OwnerUserID = user.ID
	card.Data.VoiceReference = voiceRef
	card.Data.Image = image

	cardID, err := api.db.InsertCharCard(r.Context(), card)
	if err != nil {
		characterSaveError(w, r, "InsertCharCard: "+err.Error())
		return
	}

	w.Header().Add("hx-redirect", "/characters/"+cardID.String())
	writeSaved(w, r, characterSaveStatusID, characterSaveSkip)
}

func (api *API) upsertCharacter(w http.ResponseWriter, r *http.Request) {
	characterIDStr := chi.URLParam(r, "character_id")

	user := ctxstore.GetUser(r.Context())
	if user == nil {
		characterSaveError(w, r, "not authorized")
		return
	}

	if err := r.ParseMultipartForm(20 * 1024 * 1024); err != nil {
		characterSaveError(w, r, "failed to read the form: "+err.Error())
		return
	}

	_ = r.ParseForm()

	card, err := formToCard(r.Form)
	if err != nil {
		characterSaveError(w, r, err.Error())
		return
	}

	characterID, err := uuid.Parse(characterIDStr)
	if err != nil {
		characterSaveError(w, r, "invalid character id")
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

// charImage is deliberately unauthenticated: OBS browser sources fetch it
// without a session.
func (api *API) charImage(w http.ResponseWriter, r *http.Request) {
	characterIDStr := chi.URLParam(r, "character_id")
	characterID, err := uuid.Parse(characterIDStr)
	if err != nil {
		_ = html.ExecuteTemplate(w, "error.html", &htmlErr{
			ErrorCode:    http.StatusBadRequest,
			ErrorMessage: "{character_id} is not valid uuid: " + err.Error(),
		})
		return
	}

	if box, ok := requestedBox(r); ok {
		api.charImageVariant(w, r, characterID, box)
		return
	}

	cached, err := api.imageCache.Get(r.Context(), characterID)
	if err != nil {
		_ = html.ExecuteTemplate(w, "error.html", &htmlErr{
			ErrorCode:    http.StatusInternalServerError,
			ErrorMessage: "GetCharImage: " + err.Error(),
		})
		return
	}

	if len(cached.Data) == 0 {
		servePlaceholderImage(w)
		return
	}

	// The char-data bucket stores both images and voice references as raw
	// octet-streams, so let the real type (image/*, audio/*, ...) through
	// unchanged; nosniff is the part that matters for XSS — it stops the
	// browser from ever re-sniffing stored bytes as HTML.
	ct := http.DetectContentType(cached.Data)
	if !strings.HasPrefix(ct, "image/") && !strings.HasPrefix(ct, "audio/") && !strings.HasPrefix(ct, "video/") {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(cached.Data)
}

// charImageVariant serves the card image fitted to box as WebP; the variant
// is keyed by the S3 image id, so a re-upload never serves stale pixels.
func (api *API) charImageVariant(w http.ResponseWriter, r *http.Request, characterID uuid.UUID, box fitBox) {
	imageID, err := api.db.GetCharImageID(r.Context(), characterID)
	if err != nil {
		_ = html.ExecuteTemplate(w, "error.html", &htmlErr{
			ErrorCode:    http.StatusInternalServerError,
			ErrorMessage: "GetCharImageID: " + err.Error(),
		})
		return
	}
	if imageID == "" {
		servePlaceholderImage(w)
		return
	}

	data, err := api.variants.Get(r.Context(), s3client.CharDataBucket, imageID, box, func(ctx context.Context) ([]byte, error) {
		cached, err := api.imageCache.Get(ctx, characterID)
		if err != nil {
			return nil, err
		}
		return cached.Data, nil
	})
	if err != nil {
		api.logger.Error("character image variant", "card", characterID, "box", box, "error", err)
		_ = html.ExecuteTemplate(w, "error.html", &htmlErr{
			ErrorCode:    http.StatusInternalServerError,
			ErrorMessage: "image variant: " + err.Error(),
		})
		return
	}
	if len(data) == 0 {
		servePlaceholderImage(w)
		return
	}

	w.Header().Set("Content-Type", "image/webp")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(data)
}

// requestedBox reads ?w= and ?h= and snaps each to the variant ladder. One
// missing side takes the other's value, so a plain ?w= is a square fit.
func requestedBox(r *http.Request) (fitBox, bool) {
	q := r.URL.Query()
	ws, hs := q.Get("w"), q.Get("h")
	if ws == "" && hs == "" {
		return fitBox{}, false
	}
	if ws == "" {
		ws = hs
	}
	if hs == "" {
		hs = ws
	}
	wv, errW := strconv.Atoi(ws)
	hv, errH := strconv.Atoi(hs)
	if errW != nil || errH != nil {
		return fitBox{}, false
	}
	w, okW := snapDim(wv)
	h, okH := snapDim(hv)
	if !okW || !okH {
		return fitBox{}, false
	}
	return fitBox{W: w, H: h}, true
}

func servePlaceholderImage(w http.ResponseWriter) {
	img, err := staticFS.ReadFile("static/doctorWTF.png")
	if err != nil {
		_ = html.ExecuteTemplate(w, "error.html", &htmlErr{
			ErrorCode:    http.StatusInternalServerError,
			ErrorMessage: "staticFS.ReadFile: " + err.Error(),
		})
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(img)
}

type voicesListPage struct {
	Items      []voiceItem
	TotalCount int
	NewCount   int
	Query      string
	Sort       string
	NewOnly    bool
	Filtered   bool
	PageURL    string
	ListURL    string

	Sounds        []soundItem
	TotalSounds   int
	Filters       []filterItem
	TotalFilters  int
	Emotions      []string
	Melodies      []ai.Melody
	TotalMelodies int
}

type voiceItem struct {
	ID            uuid.UUID
	Name          string
	SearchText    string
	IsNew         bool
	TrendingScore float64
	Popularity    int
	CreatedAt     time.Time
}

func normalizeVoicesSort(raw string) string {
	switch raw {
	case "newest", "popular", "az":
		return raw
	default:
		return "trending"
	}
}

func filterAndSortVoices(all []voiceItem, query, sortMode string, newOnly bool) []voiceItem {
	terms := queryTerms(query)
	out := make([]voiceItem, 0, len(all))
	for _, v := range all {
		if newOnly && !v.IsNew {
			continue
		}
		if len(terms) > 0 && !matchesQuery(v.SearchText, terms) {
			continue
		}
		out = append(out, v)
	}

	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		switch sortMode {
		case "newest":
			if !a.CreatedAt.Equal(b.CreatedAt) {
				return a.CreatedAt.After(b.CreatedAt)
			}
			return strings.ToLower(a.Name) < strings.ToLower(b.Name)
		case "popular":
			if a.Popularity != b.Popularity {
				return a.Popularity > b.Popularity
			}
			return strings.ToLower(a.Name) < strings.ToLower(b.Name)
		case "az":
			return strings.ToLower(a.Name) < strings.ToLower(b.Name)
		default:
			if a.TrendingScore != b.TrendingScore {
				return a.TrendingScore > b.TrendingScore
			}
			if a.Popularity != b.Popularity {
				return a.Popularity > b.Popularity
			}
			return strings.ToLower(a.Name) < strings.ToLower(b.Name)
		}
	})

	return out
}

func buildVoicesPage(r *http.Request, items []db.PublicShortName, pageURL, listURL string, melodies []ai.Melody) *voicesListPage {
	q := r.URL.Query()
	query := strings.TrimSpace(q.Get("q"))
	sortMode := normalizeVoicesSort(q.Get("sort"))
	newOnly := q.Get("new") == "1"

	now := time.Now()
	all := make([]voiceItem, 0, len(items))
	newCount := 0
	for _, it := range items {
		isNew := !it.CreatedAt.IsZero() && now.Sub(it.CreatedAt) <= newCardWindow
		if isNew {
			newCount++
		}
		all = append(all, voiceItem{
			ID:            it.ID,
			Name:          it.ShortCharName,
			SearchText:    buildSearchText(it.ShortCharName, it.Name, it.Description),
			IsNew:         isNew,
			TrendingScore: it.TrendingScore,
			Popularity:    it.Redeems + it.TTSRedeems,
			CreatedAt:     it.CreatedAt,
		})
	}

	return &voicesListPage{
		Items:         filterAndSortVoices(all, query, sortMode, newOnly),
		TotalCount:    len(all),
		NewCount:      newCount,
		Query:         query,
		Sort:          sortMode,
		NewOnly:       newOnly,
		Filtered:      query != "" || newOnly || sortMode != "trending",
		PageURL:       pageURL,
		ListURL:       listURL,
		Sounds:        soundItems,
		TotalSounds:   len(soundItems),
		Filters:       filterItems,
		TotalFilters:  len(filterItems),
		Emotions:      ai.EmotionNames,
		Melodies:      melodies,
		TotalMelodies: len(melodies),
	}
}

func (api *API) loadStreamerVoices(r *http.Request) ([]db.PublicShortName, string, int, error) {
	login := chi.URLParam(r, "twitch_login")
	streamer, err := api.db.GetUserByTwitchLogin(r.Context(), login)
	if err != nil {
		code := http.StatusInternalServerError
		if errors.Is(err, db.ErrNoRows) {
			code = http.StatusNotFound
			err = errors.New("unknown streamer")
		}
		return nil, login, code, err
	}

	settings, err := api.db.GetUserSettings(r.Context(), streamer.ID)
	if err != nil {
		return nil, login, http.StatusInternalServerError, err
	}

	items, err := api.db.GetPublicShortNamedCards(r.Context())
	if err != nil {
		return nil, login, http.StatusInternalServerError, err
	}

	items = slices.DeleteFunc(items, func(it db.PublicShortName) bool { return settings.CardDisabled(it.ID) })
	return items, streamer.TwitchLogin, http.StatusOK, nil
}

func (api *API) loadMelodies(r *http.Request) []ai.Melody {
	if api.singer == nil {
		return nil
	}
	m, err := api.singer.Melodies(r.Context())
	if err != nil {
		api.logger.Warn("failed to list melodies", "err", err)
		return nil
	}
	return m
}

// voicesPublic renders a public page with all public short-named cards and their images.
func (api *API) voicesPublic(r *http.Request) template.HTML {
	items, err := api.db.GetPublicShortNamedCards(r.Context())
	if err != nil {
		return getHtml("error.html", &htmlErr{
			ErrorCode:    http.StatusInternalServerError,
			ErrorMessage: err.Error(),
		})
	}

	page := buildVoicesPage(r, items, "/voices", "/voices/list/cards", api.loadMelodies(r))
	return getHtml("voices.html", page)
}

// voicesStreamer renders the voices page for one streamer's channel: every
// public voice universal TTS/AI accepts there, minus the characters the
// streamer switched off.
func (api *API) voicesStreamer(r *http.Request) template.HTML {
	items, login, code, err := api.loadStreamerVoices(r)
	if err != nil {
		return getHtml("error.html", &htmlErr{ErrorCode: code, ErrorMessage: err.Error()})
	}

	page := buildVoicesPage(r, items, "/voices/"+url.PathEscape(login), "/voices/list/channel/"+url.PathEscape(login), api.loadMelodies(r))
	return getHtml("voices.html", page)
}

func (api *API) voicesListPublic(w http.ResponseWriter, r *http.Request) {
	items, err := api.db.GetPublicShortNamedCards(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	page := buildVoicesPage(r, items, "/voices", "/voices/list/cards", nil)
	_ = html.ExecuteTemplate(w, "voices-results", page)
}

func (api *API) voicesListStreamer(w http.ResponseWriter, r *http.Request) {
	items, login, code, err := api.loadStreamerVoices(r)
	if err != nil {
		http.Error(w, err.Error(), code)
		return
	}

	page := buildVoicesPage(r, items, "/voices/"+url.PathEscape(login), "/voices/list/channel/"+url.PathEscape(login), nil)
	_ = html.ExecuteTemplate(w, "voices-results", page)
}

func (api *API) voicesListSounds(w http.ResponseWriter, r *http.Request) {
	terms := queryTerms(r.URL.Query().Get("q"))
	filtered := make([]soundItem, 0, len(soundItems))
	for _, s := range soundItems {
		hay := buildSearchText(fmt.Sprintf("[%d] %d", s.ID, s.ID), s.Name)
		if matchesQuery(hay, terms) {
			filtered = append(filtered, s)
		}
	}
	_ = html.ExecuteTemplate(w, "voices-sounds-results", &voicesListPage{
		Sounds:      filtered,
		TotalSounds: len(soundItems),
	})
}

func (api *API) voicesListFilters(w http.ResponseWriter, r *http.Request) {
	terms := queryTerms(r.URL.Query().Get("q"))
	filtered := make([]filterItem, 0, len(filterItems))
	for _, f := range filterItems {
		hay := buildSearchText(fmt.Sprintf("{%d} %d", f.ID, f.ID), f.Name)
		if matchesQuery(hay, terms) {
			filtered = append(filtered, f)
		}
	}
	_ = html.ExecuteTemplate(w, "voices-filters-results", &voicesListPage{
		Filters:      filtered,
		TotalFilters: len(filterItems),
	})
}

func (api *API) voicesListMelodies(w http.ResponseWriter, r *http.Request) {
	all := api.loadMelodies(r)
	terms := queryTerms(r.URL.Query().Get("q"))
	filtered := make([]ai.Melody, 0, len(all))
	for _, m := range all {
		hay := buildSearchText(fmt.Sprintf("{sing:%d} %d", m.Number, m.Number), m.Name)
		if matchesQuery(hay, terms) {
			filtered = append(filtered, m)
		}
	}
	_ = html.ExecuteTemplate(w, "voices-melodies-results", &voicesListPage{
		Melodies:      filtered,
		TotalMelodies: len(all),
	})
}

func (api *API) universalTTSReward(w http.ResponseWriter, r *http.Request) {
	user := ctxstore.GetUser(r.Context())
	if user == nil {
		submitTab(w, "error.html", &htmlErr{
			ErrorCode:    http.StatusUnauthorized,
			ErrorMessage: "not authorized",
		})
		return
	}

	prompt := "Voices: " + r.Host + "/voices/" + user.TwitchLogin

	if err := api.createRewardAndUpsert(r.Context(), user, nil, "", db.TwitchRewardUniversalTTS, prompt); err != nil {
		submitTab(w, "error.html", &htmlErr{
			ErrorCode:    http.StatusInternalServerError,
			ErrorMessage: err.Error(),
		})
		return
	}

	w.Header().Add("hx-redirect", "/characters")
	_, _ = w.Write([]byte("success"))
}

func (api *API) agenticReward(w http.ResponseWriter, r *http.Request) {
	user := ctxstore.GetUser(r.Context())
	if user == nil {
		submitTab(w, "error.html", &htmlErr{
			ErrorCode:    http.StatusUnauthorized,
			ErrorMessage: "not authorized",
		})
		return
	}

	if err := api.createRewardAndUpsert(r.Context(), user, nil, "", db.TwitchRewardAgentic, ""); err != nil {
		submitTab(w, "error.html", &htmlErr{
			ErrorCode:    http.StatusInternalServerError,
			ErrorMessage: err.Error(),
		})
		return
	}

	w.Header().Add("hx-redirect", "/characters")
	_, _ = w.Write([]byte("success"))
}

type rewardChooseSpecialData struct {
	Subtitle         string
	CreatePostURL    string
	ExistingPostURL  string
	Error            string
	ExistingRewardID string
}

func (api *API) universalTTSRewardChoose(r *http.Request) template.HTML {
	user := ctxstore.GetUser(r.Context())
	if user == nil {
		return getHtml("error.html", &htmlErr{
			ErrorCode:    http.StatusUnauthorized,
			ErrorMessage: "not authorized",
		})
	}

	return getHtml("reward_choose_special.html", &rewardChooseSpecialData{
		Subtitle:        "Universal TTS",
		CreatePostURL:   "/universal-tts/reward",
		ExistingPostURL: "/universal-tts/reward_existing",
	})
}

func (api *API) universalTTSRewardExisting(w http.ResponseWriter, r *http.Request) {
	user := ctxstore.GetUser(r.Context())
	if user == nil {
		submitTab(w, "error.html", &htmlErr{
			ErrorCode:    http.StatusUnauthorized,
			ErrorMessage: "not authorized",
		})
		return
	}

	if err := r.ParseForm(); err != nil {
		submitTab(w, "reward_choose_special.html", &rewardChooseSpecialData{
			Subtitle:        "Universal TTS",
			CreatePostURL:   "/universal-tts/reward",
			ExistingPostURL: "/universal-tts/reward_existing",
			Error:           "failed to parse form",
		})
		return
	}

	existingID := strings.TrimSpace(r.Form.Get("twitch_reward_id"))
	if existingID == "" {
		submitTab(w, "reward_choose_special.html", &rewardChooseSpecialData{
			Subtitle:         "Universal TTS",
			CreatePostURL:    "/universal-tts/reward",
			ExistingPostURL:  "/universal-tts/reward_existing",
			Error:            "Reward ID is required",
			ExistingRewardID: existingID,
		})
		return
	}

	if err := api.db.UpsertUniversalTTSReward(r.Context(), user.ID, existingID); err != nil {
		submitTab(w, "reward_choose_special.html", &rewardChooseSpecialData{
			Subtitle:         "Universal TTS",
			CreatePostURL:    "/universal-tts/reward",
			ExistingPostURL:  "/universal-tts/reward_existing",
			Error:            err.Error(),
			ExistingRewardID: existingID,
		})
		return
	}

	w.Header().Add("hx-redirect", "/characters")
	_, _ = w.Write([]byte("success"))
}

func (api *API) universalAIReward(w http.ResponseWriter, r *http.Request) {
	user := ctxstore.GetUser(r.Context())
	if user == nil {
		submitTab(w, "error.html", &htmlErr{
			ErrorCode:    http.StatusUnauthorized,
			ErrorMessage: "not authorized",
		})
		return
	}

	prompt := "Name a character first: forsen: tell me a joke. Characters: " + r.Host + "/voices/" + user.TwitchLogin

	if err := api.createRewardAndUpsert(r.Context(), user, nil, "", db.TwitchRewardUniversalAI, prompt); err != nil {
		submitTab(w, "error.html", &htmlErr{
			ErrorCode:    http.StatusInternalServerError,
			ErrorMessage: err.Error(),
		})
		return
	}

	w.Header().Add("hx-redirect", "/characters")
	_, _ = w.Write([]byte("success"))
}

func (api *API) universalAIRewardChoose(r *http.Request) template.HTML {
	user := ctxstore.GetUser(r.Context())
	if user == nil {
		return getHtml("error.html", &htmlErr{
			ErrorCode:    http.StatusUnauthorized,
			ErrorMessage: "not authorized",
		})
	}

	return getHtml("reward_choose_special.html", &rewardChooseSpecialData{
		Subtitle:        "Universal AI",
		CreatePostURL:   "/universal-ai/reward",
		ExistingPostURL: "/universal-ai/reward_existing",
	})
}

func (api *API) universalAIRewardExisting(w http.ResponseWriter, r *http.Request) {
	user := ctxstore.GetUser(r.Context())
	if user == nil {
		submitTab(w, "error.html", &htmlErr{
			ErrorCode:    http.StatusUnauthorized,
			ErrorMessage: "not authorized",
		})
		return
	}

	if err := r.ParseForm(); err != nil {
		submitTab(w, "reward_choose_special.html", &rewardChooseSpecialData{
			Subtitle:        "Universal AI",
			CreatePostURL:   "/universal-ai/reward",
			ExistingPostURL: "/universal-ai/reward_existing",
			Error:           "failed to parse form",
		})
		return
	}

	existingID := strings.TrimSpace(r.Form.Get("twitch_reward_id"))
	if existingID == "" {
		submitTab(w, "reward_choose_special.html", &rewardChooseSpecialData{
			Subtitle:         "Universal AI",
			CreatePostURL:    "/universal-ai/reward",
			ExistingPostURL:  "/universal-ai/reward_existing",
			Error:            "Reward ID is required",
			ExistingRewardID: existingID,
		})
		return
	}

	if err := api.db.UpsertUniversalAIReward(r.Context(), user.ID, existingID); err != nil {
		submitTab(w, "reward_choose_special.html", &rewardChooseSpecialData{
			Subtitle:         "Universal AI",
			CreatePostURL:    "/universal-ai/reward",
			ExistingPostURL:  "/universal-ai/reward_existing",
			Error:            err.Error(),
			ExistingRewardID: existingID,
		})
		return
	}

	w.Header().Add("hx-redirect", "/characters")
	_, _ = w.Write([]byte("success"))
}

func (api *API) agenticRewardChoose(r *http.Request) template.HTML {
	user := ctxstore.GetUser(r.Context())
	if user == nil {
		return getHtml("error.html", &htmlErr{
			ErrorCode:    http.StatusUnauthorized,
			ErrorMessage: "not authorized",
		})
	}

	return getHtml("reward_choose_special.html", &rewardChooseSpecialData{
		Subtitle:        "Agent Baj",
		CreatePostURL:   "/agentic/reward",
		ExistingPostURL: "/agentic/reward_existing",
	})
}

func (api *API) agenticRewardExisting(w http.ResponseWriter, r *http.Request) {
	user := ctxstore.GetUser(r.Context())
	if user == nil {
		submitTab(w, "error.html", &htmlErr{
			ErrorCode:    http.StatusUnauthorized,
			ErrorMessage: "not authorized",
		})
		return
	}

	if err := r.ParseForm(); err != nil {
		submitTab(w, "reward_choose_special.html", &rewardChooseSpecialData{
			Subtitle:        "Agent Baj",
			CreatePostURL:   "/agentic/reward",
			ExistingPostURL: "/agentic/reward_existing",
			Error:           "failed to parse form",
		})
		return
	}

	existingID := strings.TrimSpace(r.Form.Get("twitch_reward_id"))
	if existingID == "" {
		submitTab(w, "reward_choose_special.html", &rewardChooseSpecialData{
			Subtitle:         "Agent Baj",
			CreatePostURL:    "/agentic/reward",
			ExistingPostURL:  "/agentic/reward_existing",
			Error:            "Reward ID is required",
			ExistingRewardID: existingID,
		})
		return
	}

	if err := api.db.UpsertAgenticReward(r.Context(), user.ID, existingID); err != nil {
		submitTab(w, "reward_choose_special.html", &rewardChooseSpecialData{
			Subtitle:         "Agent Baj",
			CreatePostURL:    "/agentic/reward",
			ExistingPostURL:  "/agentic/reward_existing",
			Error:            err.Error(),
			ExistingRewardID: existingID,
		})
		return
	}

	w.Header().Add("hx-redirect", "/characters")
	_, _ = w.Write([]byte("success"))
}

type tryPageData struct {
	CharacterID     string
	CharacterName   string
	AgenticMode     bool
	UniversalMode   bool
	UniversalAIMode bool
	// The toggles' initial state: the user's live settings at the time the
	// page opens.
	LLMFilter   bool
	RegexFilter bool
}

func (api *API) tryPage(r *http.Request, user *db.User, data tryPageData) template.HTML {
	settings, err := api.db.GetUserSettings(r.Context(), user.ID)
	if err != nil {
		settings = &db.UserSettings{}
	}
	data.LLMFilter = !settings.DisableLLMFilter
	data.RegexFilter = !settings.DisableRegexFilter
	return getHtml("try_character.html", &data)
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

	return api.tryPage(r, user, tryPageData{
		CharacterID:   characterID.String(),
		CharacterName: card.Name,
	})
}

type tryAction struct {
	Action string `json:"action"`
	Text   string `json:"text"`
	// The page's toggles. nil is a cached older script: the LLM filter stays
	// off as try runs always had it, the regex filter follows the account
	// setting.
	LLMFilter   *bool `json:"llm_filter"`
	RegexFilter *bool `json:"regex_filter"`
}

type tryJob struct {
	Action string
	Input  processor.InteractionInput
}

type TryHandler interface {
	Handle(ctx context.Context, input processor.InteractionInput, writer conns.EventWriter) error
}

func (api *API) readTryCommands(ctx context.Context, wsClient *ws.Client, state *processor.ProcessorState, eventWriter conns.EventWriter, audioWriter conns.AudioWriter, card *db.Card, dbUser *db.User) <-chan tryJob {
	jobCh := make(chan tryJob)

	go func() {
		defer tools.LogPanic(api.logger, "try ws reader")
		defer close(jobCh)

		for {
			msg, err := wsClient.Read()
			if err != nil {
				if !errors.Is(err, ws.ErrClosed) {
					api.logger.Error("failed to read from ws", "err", err)
				}
				return
			}

			var action tryAction
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

			userSettings, err := api.db.GetUserSettings(ctx, dbUser.ID)
			if err != nil {
				api.logger.Warn("failed to get user settings, using defaults", "err", err)
				userSettings = &db.UserSettings{}
			}

			// the toggles override the account settings in both directions, and
			// handlers consult the settings, so the copy carries their values
			llmFilter := action.LLMFilter != nil && *action.LLMFilter
			actionSettings := *userSettings
			actionSettings.DisableLLMFilter = !llmFilter
			if action.RegexFilter != nil {
				actionSettings.DisableRegexFilter = !*action.RegexFilter
			}

			input := processor.InteractionInput{
				Requester:          dbUser.TwitchLogin,
				Broadcaster:        dbUser,
				Message:            action.Text,
				Character:          card,
				UserSettings:       &actionSettings,
				MsgID:              msgID.String(),
				SkipLLMFilterFully: !llmFilter,
				State:              state,
				AudioWriter:        audioWriter,
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

	// audio frames go straight to this try socket as raw binary (Send is
	// mutex-protected); the page tells them apart from the JSON envelope by
	// the first byte
	audioWriter := conns.AudioWriter(func(frame []byte) bool {
		if err := wsClient.Send(&ws.Message{
			MsgType: websocket.BinaryMessage,
			Message: frame,
		}); err != nil {
			logger.Warn("failed to send audio frame to try ws", "err", err)
			return false
		}
		return true
	})

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	state := processor.NewProcessorState()

	wg.Add(1)
	go func() {
		defer tools.LogPanic(logger, "try ws sender")
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

	jobCh := api.readTryCommands(ctx, wsClient, state, eventWriter, audioWriter, card, user)

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

	return api.tryPage(r, user, tryPageData{
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

func (api *API) tryUniversalTTS(r *http.Request) template.HTML {
	user := ctxstore.GetUser(r.Context())
	if user == nil {
		return getHtml("error.html", &htmlErr{
			ErrorCode:    http.StatusUnauthorized,
			ErrorMessage: "not logged in",
		})
	}

	return api.tryPage(r, user, tryPageData{
		CharacterName: "Universal TTS",
		UniversalMode: true,
	})
}

func (api *API) tryUniversalTTSWS(w http.ResponseWriter, r *http.Request) {
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
		"universal_tts": api.universalHandler,
	})
}

func (api *API) tryUniversalAI(r *http.Request) template.HTML {
	user := ctxstore.GetUser(r.Context())
	if user == nil {
		return getHtml("error.html", &htmlErr{
			ErrorCode:    http.StatusUnauthorized,
			ErrorMessage: "not logged in",
		})
	}

	return api.tryPage(r, user, tryPageData{
		CharacterName:   "Universal AI",
		UniversalAIMode: true,
	})
}

func (api *API) tryUniversalAIWS(w http.ResponseWriter, r *http.Request) {
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
		"universal_ai": api.universalAIHandler,
	})
}
