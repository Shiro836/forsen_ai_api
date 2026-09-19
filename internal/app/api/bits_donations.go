package api

import (
	"fmt"
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
	db.MsgClassSub:      "Subs",
	db.MsgClassReward:   "Channel points",
	db.MsgClassRaid:     "Raids",
	db.MsgClassStreak:   "Streaks",
	db.MsgClassFollow:   "Follows",
	db.MsgClassChat:     "Chat TTS",
}

var eventActions = []db.TwitchRewardType{db.TwitchRewardUniversalTTS, db.TwitchRewardTTS, db.TwitchRewardAI}

// Channel points and chat have nothing to choose, so they get no card.
var eventCards = []db.MsgClass{db.MsgClassBits, db.MsgClassDonation, db.MsgClassSub, db.MsgClassRaid, db.MsgClassStreak, db.MsgClassFollow}

type bdLineField struct {
	Line  db.EventLine
	Label string
}

var eventCardLines = map[db.MsgClass][]bdLineField{
	db.MsgClassSub:    {{db.EventLineSub, "new sub"}, {db.EventLineResub, "resub"}, {db.EventLineGift, "gift subs"}},
	db.MsgClassRaid:   {{db.EventLineRaid, ""}},
	db.MsgClassStreak: {{db.EventLineStreak, ""}},
	db.MsgClassFollow: {{db.EventLineFollow, ""}},
}

// bdState is everything the page edits. It travels in the form, so nothing
// reaches the streamer's settings before Save.
type bdState struct {
	Order       [][]db.MsgClass
	Enabled     map[db.MsgClass]bool
	Actions     map[db.MsgClass]db.EventAction
	Lines       map[db.EventLine]string
	FollowsStay bool
}

func bdStateFromSettings(settings *db.UserSettings) *bdState {
	state := &bdState{
		Order:       settings.PlayOrder(),
		Enabled:     make(map[db.MsgClass]bool),
		Actions:     make(map[db.MsgClass]db.EventAction),
		Lines:       make(map[db.EventLine]string),
		FollowsStay: settings.FollowsStay,
	}
	for class := range laneNames {
		state.Enabled[class] = settings.LaneEnabled(class)
	}
	for _, class := range eventCards {
		state.Actions[class] = settings.EventAction(class)
		for _, field := range eventCardLines[class] {
			state.Lines[field.Line] = settings.EventLine(field.Line)
		}
	}
	return state
}

func formatBDOrder(order [][]db.MsgClass) string {
	groups := make([]string, len(order))
	for i, group := range order {
		classes := make([]string, len(group))
		for j, class := range group {
			classes[j] = string(class)
		}
		groups[i] = strings.Join(classes, "+")
	}
	return strings.Join(groups, ",")
}

func parseBDOrder(value string) [][]db.MsgClass {
	var order [][]db.MsgClass
	for _, group := range strings.Split(value, ",") {
		var classes []db.MsgClass
		for _, class := range strings.Split(group, "+") {
			classes = append(classes, db.MsgClass(class))
		}
		order = append(order, classes)
	}
	return order
}

func bdStateFromForm(r *http.Request) (*bdState, error) {
	state := &bdState{
		Order:       parseBDOrder(r.Form.Get("order")),
		Enabled:     make(map[db.MsgClass]bool),
		Actions:     make(map[db.MsgClass]db.EventAction),
		Lines:       make(map[db.EventLine]string),
		FollowsStay: r.Form.Get("follows_yield") != "on",
	}
	if err := (&db.UserSettings{}).SetPlayOrder(state.Order); err != nil {
		return nil, err
	}

	for class := range laneNames {
		state.Enabled[class] = slices.Contains(r.Form["on"], string(class))
	}

	for _, class := range eventCards {
		rewardType, err := strconv.Atoi(r.Form.Get("action_" + string(class)))
		if err != nil || !slices.Contains(eventActions, db.TwitchRewardType(rewardType)) {
			return nil, fmt.Errorf("invalid action for %s", class)
		}
		action := db.EventAction{RewardType: db.TwitchRewardType(rewardType)}
		if value := r.Form.Get("card_" + string(class)); value != "" {
			cardID, err := uuid.Parse(value)
			if err != nil {
				return nil, fmt.Errorf("invalid character for %s", class)
			}
			action.CardID = &cardID
		}
		state.Actions[class] = action

		for _, field := range eventCardLines[class] {
			text := strings.Join(strings.Fields(r.Form.Get("line_"+string(field.Line))), " ")
			if text == "" {
				text = field.Line.Default()
			}
			state.Lines[field.Line] = text
		}
	}

	return state, nil
}

func groupPaid(group []db.MsgClass) bool {
	return !slices.ContainsFunc(group, func(class db.MsgClass) bool { return !class.Paid() })
}

// edit applies one click to the state and reports which card's character
// picker the click asked to open.
func (s *bdState) edit(r *http.Request) (picker db.MsgClass, err error) {
	class := db.MsgClass(r.Form.Get("class"))

	switch op := r.Form.Get("op"); op {
	case "up", "down", "merge", "split":
		i, err := strconv.Atoi(r.Form.Get("group"))
		if err != nil || i < 0 || i >= len(s.Order) {
			return "", fmt.Errorf("invalid group")
		}
		switch {
		case op == "up" && i > 0:
			s.Order[i-1], s.Order[i] = s.Order[i], s.Order[i-1]
		case op == "down" && i+1 < len(s.Order):
			s.Order[i], s.Order[i+1] = s.Order[i+1], s.Order[i]
		case op == "merge" && i+1 < len(s.Order) && groupPaid(s.Order[i]) && groupPaid(s.Order[i+1]):
			s.Order[i] = append(s.Order[i], s.Order[i+1]...)
			s.Order = slices.Delete(s.Order, i+1, i+2)
		case op == "split" && len(s.Order[i]) > 1:
			singles := make([][]db.MsgClass, 0, len(s.Order[i]))
			for _, class := range s.Order[i] {
				singles = append(singles, []db.MsgClass{class})
			}
			s.Order = slices.Replace(s.Order, i, i+1, singles...)
		default:
			return "", fmt.Errorf("invalid move")
		}

	case "preset":
		preset := bdPresetByName(r.Form.Get("preset"))
		if preset == nil || len(eventCardLines[class]) == 0 {
			return "", fmt.Errorf("unknown preset")
		}
		for _, field := range eventCardLines[class] {
			s.Lines[field.Line] = preset.Lines[field.Line]
		}

	case "picker":
		if !slices.Contains(eventCards, class) {
			return "", fmt.Errorf("unknown event")
		}
		return class, nil

	case "character":
		cardID, err := uuid.Parse(r.Form.Get("card_id"))
		if err != nil || !slices.Contains(eventCards, class) {
			return "", fmt.Errorf("invalid character")
		}
		action := s.Actions[class]
		action.CardID = &cardID
		s.Actions[class] = action
	}

	return "", nil
}

type bdErrors struct {
	Actions map[db.MsgClass]string
	Lines   map[db.EventLine]string
}

func (e bdErrors) any() bool {
	return len(e.Actions) > 0 || len(e.Lines) > 0
}

// bdStore writes the state into settings, which the caller saves only when no
// error came back.
func (api *API) bdStore(r *http.Request, user *db.User, settings *db.UserSettings, state *bdState) bdErrors {
	errs := bdErrors{Actions: make(map[db.MsgClass]string), Lines: make(map[db.EventLine]string)}

	_ = settings.SetPlayOrder(state.Order)
	settings.FollowsStay = state.FollowsStay
	for class, enabled := range state.Enabled {
		settings.SetLaneEnabled(class, enabled)
	}
	for line, text := range state.Lines {
		if err := settings.SetEventLine(line, text); err != nil {
			errs.Lines[line] = err.Error()
		}
	}
	for class, action := range state.Actions {
		if err := settings.SetEventAction(class, action); err != nil {
			errs.Actions[class] = err.Error()
			continue
		}
		if action.RewardType != db.TwitchRewardUniversalTTS {
			if _, err := api.db.GetCharCardByID(r.Context(), user.ID, *action.CardID); err != nil {
				errs.Actions[class] = "character not found"
			}
		}
	}

	return errs
}

type bdLane struct {
	Class   db.MsgClass
	Name    string
	Enabled bool
}

type bdGroup struct {
	Index        int
	Rank         int
	Lanes        []bdLane
	Enabled      bool
	First        bool
	Last         bool
	CanMergeNext bool
}

type bdOrder struct {
	Value  string
	Groups []bdGroup
	Chat   bdLane
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
	Query      string
	Characters []bdCharacter
}

type bdLine struct {
	Line         db.EventLine
	Label        string
	Text         string
	Placeholders []string
	Error        string
}

type bdEvent struct {
	Class          db.MsgClass
	Name           string
	Enabled        bool
	Actions        []bdActionOption
	NeedsCharacter bool
	CardID         string
	Character      *bdCharacter
	Picker         *bdPicker
	ActionError    string
	CanYield       bool
	Yields         bool
	Lines          []*bdLine
	Presets        []string
}

const bdSaveStatusID = "bd_save_result"

type bdPage struct {
	Order  bdOrder
	Events []*bdEvent
	Save   saveStatus
}

func newBDOrder(state *bdState) bdOrder {
	lane := func(class db.MsgClass) bdLane {
		return bdLane{Class: class, Name: laneNames[class], Enabled: state.Enabled[class]}
	}

	order := bdOrder{Value: formatBDOrder(state.Order), Chat: lane(db.MsgClassChat)}
	for i, classes := range state.Order {
		group := bdGroup{
			Index:        i,
			Rank:         i + 1,
			First:        i == 0,
			Last:         i == len(state.Order)-1,
			CanMergeNext: i+1 < len(state.Order) && groupPaid(classes) && groupPaid(state.Order[i+1]),
		}
		for _, class := range classes {
			group.Lanes = append(group.Lanes, lane(class))
			group.Enabled = group.Enabled || state.Enabled[class]
		}
		order.Groups = append(order.Groups, group)
	}
	return order
}

func (api *API) newBDEvent(r *http.Request, user *db.User, settings *db.UserSettings, state *bdState, class db.MsgClass, openPicker bool, errs bdErrors) (*bdEvent, error) {
	action := state.Actions[class]

	event := &bdEvent{
		Class:          class,
		Name:           laneNames[class],
		Enabled:        state.Enabled[class],
		NeedsCharacter: action.RewardType != db.TwitchRewardUniversalTTS,
		ActionError:    errs.Actions[class],
	}
	for _, field := range eventCardLines[class] {
		event.Lines = append(event.Lines, &bdLine{
			Line:         field.Line,
			Label:        field.Label,
			Text:         state.Lines[field.Line],
			Placeholders: field.Line.Placeholders(),
			Error:        errs.Lines[field.Line],
		})
	}
	if len(event.Lines) > 0 {
		event.Presets = bdPresetNames()
	}
	if class == db.MsgClassFollow {
		event.CanYield = true
		event.Yields = !state.FollowsStay
	}
	for _, rewardType := range eventActions {
		event.Actions = append(event.Actions, bdActionOption{
			Value:   int(rewardType),
			Label:   rewardType.String(),
			Checked: rewardType == action.RewardType,
		})
	}

	if action.CardID != nil {
		event.CardID = action.CardID.String()
		if card, err := api.db.GetCharCardByID(r.Context(), user.ID, *action.CardID); err == nil {
			event.Character = &bdCharacter{ID: card.ID, Name: card.Name}
		}
	}

	if event.NeedsCharacter && (openPicker || event.Character == nil) {
		picker, err := api.newBDPicker(r, user, settings, class, "")
		if err != nil {
			return nil, err
		}
		event.Picker = picker
	}

	return event, nil
}

func (api *API) newBDPicker(r *http.Request, user *db.User, settings *db.UserSettings, class db.MsgClass, query string) (*bdPicker, error) {
	cards, err := api.db.GetCharCards(r.Context(), user.ID, db.GetChatCardsParams{
		ShowPublic: true,
		SortBy:     db.SortByNewest,
	})
	if err != nil {
		return nil, err
	}

	query = strings.TrimSpace(query)
	needle := strings.ToLower(query)

	picker := &bdPicker{Class: class, Query: query}
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

// The page is re-rendered on every click, so its status always asks again
// (Check) whether the new form still matches the baseline it carries along.
func (api *API) newBDPage(r *http.Request, user *db.User, settings *db.UserSettings, state *bdState, picker db.MsgClass, errs bdErrors) (*bdPage, error) {
	page := &bdPage{
		Order: newBDOrder(state),
		Save:  saveStatus{ID: bdSaveStatusID, Baseline: r.Form.Get("_baseline"), Check: true},
	}
	if errs.any() {
		page.Save.Check = false
		page.Save.Error = "not saved, fix the marked fields"
	}
	for _, class := range eventCards {
		event, err := api.newBDEvent(r, user, settings, state, class, class == picker, errs)
		if err != nil {
			return nil, err
		}
		page.Events = append(page.Events, event)
	}
	return page, nil
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

	page, err := api.newBDPage(r, user, settings, bdStateFromSettings(settings), "", bdErrors{})
	if err != nil {
		return getHtml("error.html", &htmlErr{ErrorCode: http.StatusInternalServerError, ErrorMessage: "failed to load characters: " + err.Error()})
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

func (api *API) bdRender(w http.ResponseWriter, r *http.Request, user *db.User, settings *db.UserSettings, state *bdState, picker db.MsgClass, errs bdErrors, saved bool) {
	page, err := api.newBDPage(r, user, settings, state, picker, errs)
	if err != nil {
		http.Error(w, "failed to load characters: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if saved {
		// no baseline: the saved form becomes the new one on its first check
		page.Save.Baseline = ""
		page.Save.Saved = true
	}
	_ = html.ExecuteTemplate(w, "bd-page", page)
}

func (api *API) bitsDonationsEdit(w http.ResponseWriter, r *http.Request) {
	user, settings, ok := api.bdContext(w, r)
	if !ok {
		return
	}

	state, err := bdStateFromForm(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	picker, err := state.edit(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	api.bdRender(w, r, user, settings, state, picker, bdErrors{}, false)
}

func (api *API) bitsDonationsSave(w http.ResponseWriter, r *http.Request) {
	user, settings, ok := api.bdContext(w, r)
	if !ok {
		return
	}

	state, err := bdStateFromForm(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	errs := api.bdStore(r, user, settings, state)
	if errs.any() {
		api.bdRender(w, r, user, settings, state, "", errs, false)
		return
	}

	if err := api.db.UpdateUserData(r.Context(), user.ID, settings); err != nil {
		http.Error(w, "failed to save: "+err.Error(), http.StatusInternalServerError)
		return
	}

	api.bdRender(w, r, user, settings, bdStateFromSettings(settings), "", bdErrors{}, true)
}

func (api *API) bitsDonationsPicker(w http.ResponseWriter, r *http.Request) {
	user, settings, ok := api.bdContext(w, r)
	if !ok {
		return
	}

	class := db.MsgClass(chi.URLParam(r, "event"))
	if !slices.Contains(eventCards, class) {
		http.Error(w, "unknown event", http.StatusNotFound)
		return
	}

	picker, err := api.newBDPicker(r, user, settings, class, r.Form.Get("q"))
	if err != nil {
		http.Error(w, "failed to load characters: "+err.Error(), http.StatusInternalServerError)
		return
	}
	_ = html.ExecuteTemplate(w, "bd-picker-grid", picker)
}
