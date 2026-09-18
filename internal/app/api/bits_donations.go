package api

import (
	"html/template"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"app/db"
	"app/pkg/ctxstore"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

var laneNames = map[db.MsgClass]string{
	db.MsgClassDonation: "Donations",
	db.MsgClassBits:     "Bits",
	db.MsgClassReward:   "Channel points",
}

var eventActions = []db.TwitchRewardType{db.TwitchRewardUniversalTTS, db.TwitchRewardTTS, db.TwitchRewardAI}

type bdLane struct {
	Class db.MsgClass
	Name  string
	Rank  int
	First bool
	Last  bool
}

type bdOrder struct {
	Lanes []bdLane
}

type bdActionOption struct {
	Value   int
	Label   string
	Checked bool
}

type bdCharacter struct {
	ID   uuid.UUID
	Name string
}

type bdPicker struct {
	Class      db.MsgClass
	RewardType int
	Query      string
	Characters []bdCharacter
}

type bdEvent struct {
	Class          db.MsgClass
	Name           string
	RewardType     int
	Actions        []bdActionOption
	NeedsCharacter bool
	Character      *bdCharacter
	Picker         *bdPicker
}

type bdPage struct {
	Order  bdOrder
	Events []*bdEvent
}

func newBDOrder(settings *db.UserSettings) bdOrder {
	order := settings.PlayOrder()
	lanes := make([]bdLane, 0, len(order))
	for i, class := range order {
		lanes = append(lanes, bdLane{
			Class: class,
			Name:  laneNames[class],
			Rank:  i + 1,
			First: i == 0,
			Last:  i == len(order)-1,
		})
	}
	return bdOrder{Lanes: lanes}
}

func (api *API) newBDEvent(r *http.Request, user *db.User, settings *db.UserSettings, class db.MsgClass, action db.EventAction, openPicker bool) (*bdEvent, error) {
	event := &bdEvent{
		Class:          class,
		Name:           laneNames[class],
		RewardType:     int(action.RewardType),
		NeedsCharacter: action.RewardType != db.TwitchRewardUniversalTTS,
	}
	for _, rewardType := range eventActions {
		event.Actions = append(event.Actions, bdActionOption{
			Value:   int(rewardType),
			Label:   rewardType.String(),
			Checked: rewardType == action.RewardType,
		})
	}

	if !event.NeedsCharacter {
		return event, nil
	}

	if action.CardID != nil {
		card, err := api.db.GetCharCardByID(r.Context(), user.ID, *action.CardID)
		if err == nil {
			event.Character = &bdCharacter{ID: card.ID, Name: card.Name}
		}
	}

	if openPicker || event.Character == nil {
		picker, err := api.newBDPicker(r, user, settings, class, action.RewardType, "")
		if err != nil {
			return nil, err
		}
		event.Picker = picker
	}

	return event, nil
}

func (api *API) newBDPicker(r *http.Request, user *db.User, settings *db.UserSettings, class db.MsgClass, rewardType db.TwitchRewardType, query string) (*bdPicker, error) {
	cards, err := api.db.GetCharCards(r.Context(), user.ID, db.GetChatCardsParams{
		ShowPublic: true,
		SortBy:     db.SortByNewest,
	})
	if err != nil {
		return nil, err
	}

	query = strings.TrimSpace(query)
	needle := strings.ToLower(query)

	picker := &bdPicker{Class: class, RewardType: int(rewardType), Query: query}
	for _, card := range cards {
		if settings.CardDisabled(card.ID) {
			continue
		}
		if needle != "" && !strings.Contains(strings.ToLower(card.Name), needle) {
			continue
		}
		picker.Characters = append(picker.Characters, bdCharacter{ID: card.ID, Name: card.Name})
	}

	return picker, nil
}

func (api *API) bitsDonations(r *http.Request) template.HTML {
	user := ctxstore.GetUser(r.Context())
	if user == nil {
		return getHtml("error.html", &htmlErr{ErrorCode: http.StatusUnauthorized, ErrorMessage: "unauthorized"})
	}

	settings, err := api.db.GetUserSettings(r.Context(), user.ID)
	if err != nil {
		return getHtml("error.html", &htmlErr{ErrorCode: http.StatusInternalServerError, ErrorMessage: "failed to get user settings: " + err.Error()})
	}

	page := &bdPage{Order: newBDOrder(settings)}
	for _, class := range []db.MsgClass{db.MsgClassBits, db.MsgClassDonation} {
		event, err := api.newBDEvent(r, user, settings, class, settings.EventAction(class), false)
		if err != nil {
			return getHtml("error.html", &htmlErr{ErrorCode: http.StatusInternalServerError, ErrorMessage: "failed to load characters: " + err.Error()})
		}
		page.Events = append(page.Events, event)
	}

	return getHtml("bits_donations.html", page)
}

// On false the error response is already written.
func (api *API) bdContext(w http.ResponseWriter, r *http.Request) (*db.User, *db.UserSettings, bool) {
	user := ctxstore.GetUser(r.Context())
	if user == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return nil, nil, false
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "failed to parse form: "+err.Error(), http.StatusBadRequest)
		return nil, nil, false
	}
	settings, err := api.db.GetUserSettings(r.Context(), user.ID)
	if err != nil {
		http.Error(w, "failed to get user settings: "+err.Error(), http.StatusInternalServerError)
		return nil, nil, false
	}
	return user, settings, true
}

func bdEventClass(w http.ResponseWriter, r *http.Request) (db.MsgClass, bool) {
	class := db.MsgClass(chi.URLParam(r, "event"))
	if class != db.MsgClassBits && class != db.MsgClassDonation {
		http.Error(w, "unknown event", http.StatusNotFound)
		return "", false
	}
	return class, true
}

func bdRewardType(w http.ResponseWriter, r *http.Request) (db.TwitchRewardType, bool) {
	value, err := strconv.Atoi(r.Form.Get("reward_type"))
	if err != nil || !slices.Contains(eventActions, db.TwitchRewardType(value)) {
		http.Error(w, "invalid action", http.StatusBadRequest)
		return 0, false
	}
	return db.TwitchRewardType(value), true
}

func (api *API) bdRenderEvent(w http.ResponseWriter, r *http.Request, user *db.User, settings *db.UserSettings, class db.MsgClass, action db.EventAction, openPicker bool) {
	event, err := api.newBDEvent(r, user, settings, class, action, openPicker)
	if err != nil {
		http.Error(w, "failed to load characters: "+err.Error(), http.StatusInternalServerError)
		return
	}
	_ = html.ExecuteTemplate(w, "bd-event", event)
}

func (api *API) bdSaveAction(w http.ResponseWriter, r *http.Request, user *db.User, settings *db.UserSettings, class db.MsgClass, action db.EventAction) bool {
	if settings.EventActions == nil {
		settings.EventActions = make(map[db.MsgClass]*db.EventAction)
	}
	settings.EventActions[class] = &action
	if err := api.db.UpdateUserData(r.Context(), user.ID, settings); err != nil {
		http.Error(w, "failed to save: "+err.Error(), http.StatusInternalServerError)
		return false
	}
	return true
}

func (api *API) bitsDonationsMove(w http.ResponseWriter, r *http.Request) {
	user, settings, ok := api.bdContext(w, r)
	if !ok {
		return
	}

	order := settings.PlayOrder()
	from := slices.Index(order, db.MsgClass(r.Form.Get("lane")))
	to := from + 1
	if r.Form.Get("dir") == "up" {
		to = from - 1
	}
	if from < 0 || to < 0 || to >= len(order) {
		http.Error(w, "invalid move", http.StatusBadRequest)
		return
	}
	order[from], order[to] = order[to], order[from]

	settings.QueueOrder = order
	if err := api.db.UpdateUserData(r.Context(), user.ID, settings); err != nil {
		http.Error(w, "failed to save: "+err.Error(), http.StatusInternalServerError)
		return
	}

	_ = html.ExecuteTemplate(w, "bd-order", newBDOrder(settings))
}

func (api *API) bitsDonationsAction(w http.ResponseWriter, r *http.Request) {
	user, settings, ok := api.bdContext(w, r)
	if !ok {
		return
	}
	class, ok := bdEventClass(w, r)
	if !ok {
		return
	}

	rewardType, ok := bdRewardType(w, r)
	if !ok {
		return
	}

	action := settings.EventAction(class)
	action.RewardType = rewardType

	// a character action is stored only once its character is picked
	if action.Complete() && !api.bdSaveAction(w, r, user, settings, class, action) {
		return
	}

	api.bdRenderEvent(w, r, user, settings, class, action, false)
}

func (api *API) bitsDonationsCharacter(w http.ResponseWriter, r *http.Request) {
	user, settings, ok := api.bdContext(w, r)
	if !ok {
		return
	}
	class, ok := bdEventClass(w, r)
	if !ok {
		return
	}

	cardID, err := uuid.Parse(r.Form.Get("card_id"))
	if err != nil {
		http.Error(w, "invalid character", http.StatusBadRequest)
		return
	}
	if _, err := api.db.GetCharCardByID(r.Context(), user.ID, cardID); err != nil {
		http.Error(w, "character not found", http.StatusNotFound)
		return
	}

	rewardType, ok := bdRewardType(w, r)
	if !ok {
		return
	}

	action := db.EventAction{RewardType: rewardType, CardID: &cardID}
	if !api.bdSaveAction(w, r, user, settings, class, action) {
		return
	}

	api.bdRenderEvent(w, r, user, settings, class, action, false)
}

func (api *API) bitsDonationsPicker(w http.ResponseWriter, r *http.Request) {
	user, settings, ok := api.bdContext(w, r)
	if !ok {
		return
	}
	class, ok := bdEventClass(w, r)
	if !ok {
		return
	}

	rewardType, ok := bdRewardType(w, r)
	if !ok {
		return
	}

	if !r.Form.Has("q") {
		action := settings.EventAction(class)
		action.RewardType = rewardType
		api.bdRenderEvent(w, r, user, settings, class, action, true)
		return
	}

	picker, err := api.newBDPicker(r, user, settings, class, rewardType, r.Form.Get("q"))
	if err != nil {
		http.Error(w, "failed to load characters: "+err.Error(), http.StatusInternalServerError)
		return
	}
	_ = html.ExecuteTemplate(w, "bd-picker-grid", picker)
}
