package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"app/db"
	"app/pkg/charassist"
	"app/pkg/ctxstore"

	"github.com/google/uuid"
)

const (
	assistRequestTimeout    = 90 * time.Second
	assistDefaultExamplesCt = 3
	assistSessionTTL        = 2 * time.Hour
	assistMaxUndoSnapshots  = 20
	assistMaxDisplayTurns   = 30
	assistMaxSessionsTotal  = 500
)

// CharacterAssistant powers the interactive character-editor side panel.
type CharacterAssistant interface {
	Chat(ctx context.Context, history []charassist.ChatMessage, userMessage string, current charassist.Draft) (*charassist.ChatResult, error)
	GenerateExamples(ctx context.Context, current charassist.Draft, count int) ([]db.MessageExample, error)
	HasSearch() bool
}

type charFieldData struct {
	Value string
	OOB   bool
}

type charExamplesData struct {
	Examples *msgExamples
	OOB      bool
}

type charAssistTurn struct {
	User      string
	Assistant string
	Actions   []charassist.ToolAction
}

type charAssistPanelData struct {
	SessionID    string
	HasSearch    bool
	Turns        []charAssistTurn
	CanUndo      bool
	Error        string
	DraftMessage string
}

type charAssistResponseData struct {
	Panel       *charAssistPanelData
	CharName    *charFieldData
	CharDesc    *charFieldData
	Name        *charFieldData
	FirstMsg    *charFieldData
	Personality *charFieldData
	Description *charFieldData
	Examples    *charExamplesData
	SaveStatus  *saveStatus
}

type charAssistSession struct {
	userID    uuid.UUID
	updatedAt time.Time
	history   []charassist.ChatMessage
	turns     []charAssistTurn
	undoStack []charassist.Draft
}

type charAssistSessionStore struct {
	mu       sync.Mutex
	sessions map[string]*charAssistSession
}

func newCharAssistSessionStore() *charAssistSessionStore {
	return &charAssistSessionStore{
		sessions: make(map[string]*charAssistSession),
	}
}

func (s *charAssistSessionStore) getOrCreate(sessionID string, userID uuid.UUID) (string, *charAssistSession) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	if len(s.sessions) > assistMaxSessionsTotal {
		for id, sess := range s.sessions {
			if now.Sub(sess.updatedAt) > assistSessionTTL {
				delete(s.sessions, id)
			}
		}
	}

	if sessionID != "" {
		if sess, ok := s.sessions[sessionID]; ok && sess.userID == userID {
			sess.updatedAt = now
			return sessionID, sess
		}
	}

	if sessionID == "" {
		sessionID = uuid.New().String()
	} else if _, exists := s.sessions[sessionID]; exists {
		// Never collide with another user's session id.
		sessionID = uuid.New().String()
	}
	sess := &charAssistSession{
		userID:    userID,
		updatedAt: now,
	}
	s.sessions[sessionID] = sess
	return sessionID, sess
}

func (s *charAssistSessionStore) reset(sessionID string, userID uuid.UUID) string {
	s.mu.Lock()
	defer s.mu.Unlock()

	if sess, ok := s.sessions[sessionID]; ok && sess.userID == userID {
		delete(s.sessions, sessionID)
	}
	return uuid.New().String()
}

func (s *charAssistSessionStore) snapshotState(sessionID string, userID uuid.UUID) (history []charassist.ChatMessage, turns []charAssistTurn, canUndo bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	sess, ok := s.sessions[sessionID]
	if !ok || sess.userID != userID {
		return nil, nil, false
	}
	history = append([]charassist.ChatMessage(nil), sess.history...)
	turns = append([]charAssistTurn(nil), sess.turns...)
	return history, turns, len(sess.undoStack) > 0
}

func (s *charAssistSessionStore) commitTurn(sessionID string, userID uuid.UUID, turn charAssistTurn, pushHistory bool, undoPrev *charassist.Draft) (turns []charAssistTurn, canUndo bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	sess, ok := s.sessions[sessionID]
	if !ok || sess.userID != userID {
		sess = &charAssistSession{userID: userID}
		s.sessions[sessionID] = sess
	}
	sess.updatedAt = time.Now()
	sess.turns = append(sess.turns, turn)
	if len(sess.turns) > assistMaxDisplayTurns {
		sess.turns = sess.turns[len(sess.turns)-assistMaxDisplayTurns:]
	}
	if pushHistory {
		sess.history = append(sess.history,
			charassist.ChatMessage{Role: "user", Content: turn.User},
			charassist.ChatMessage{Role: "assistant", Content: turn.Assistant},
		)
	}
	if undoPrev != nil {
		sess.undoStack = append(sess.undoStack, *undoPrev)
		if len(sess.undoStack) > assistMaxUndoSnapshots {
			sess.undoStack = sess.undoStack[len(sess.undoStack)-assistMaxUndoSnapshots:]
		}
	}
	return append([]charAssistTurn(nil), sess.turns...), len(sess.undoStack) > 0
}

func (s *charAssistSessionStore) popUndo(sessionID string, userID uuid.UUID) (prev *charassist.Draft, turns []charAssistTurn, canUndo bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	sess, ok := s.sessions[sessionID]
	if !ok || sess.userID != userID || len(sess.undoStack) == 0 {
		if ok && sess.userID == userID {
			return nil, append([]charAssistTurn(nil), sess.turns...), false
		}
		return nil, nil, false
	}
	sess.updatedAt = time.Now()
	last := sess.undoStack[len(sess.undoStack)-1]
	sess.undoStack = sess.undoStack[:len(sess.undoStack)-1]
	return &last, append([]charAssistTurn(nil), sess.turns...), len(sess.undoStack) > 0
}

func (api *API) assistSessionStore() *charAssistSessionStore {
	if api.charAssistSessions == nil {
		api.charAssistSessions = newCharAssistSessionStore()
	}
	return api.charAssistSessions
}

func (api *API) assistCharacterChat(w http.ResponseWriter, r *http.Request) {
	user := ctxstore.GetUser(r.Context())
	if user == nil {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if api.charAssist == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}

	if err := parseAnyForm(r); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	store := api.assistSessionStore()
	sessionID, _ := store.getOrCreate(strings.TrimSpace(r.Form.Get("session_id")), user.ID)
	history, turns, canUndo := store.snapshotState(sessionID, user.ID)

	message := strings.TrimSpace(r.Form.Get("message"))
	if message == "" {
		api.renderAssistResponse(w, &charAssistResponseData{
			Panel: &charAssistPanelData{
				SessionID: sessionID,
				HasSearch: api.charAssist.HasSearch(),
				Turns:     turns,
				CanUndo:   canUndo,
				Error:     "Type a message first.",
			},
		})
		return
	}

	current := formToDraft(r.Form)

	ctx, cancel := context.WithTimeout(r.Context(), assistRequestTimeout)
	defer cancel()

	res, err := api.charAssist.Chat(ctx, history, message, current)
	if err != nil {
		errMsg := "Something went wrong. Try again."
		if errors.Is(err, charassist.ErrEmptyInput) {
			errMsg = err.Error()
		} else {
			api.logger.Error("character assist chat failed", "user", user.TwitchLogin, "err", err)
		}
		api.renderAssistResponse(w, &charAssistResponseData{
			Panel: &charAssistPanelData{
				SessionID:    sessionID,
				HasSearch:    api.charAssist.HasSearch(),
				Turns:        turns,
				CanUndo:      canUndo,
				Error:        errMsg,
				DraftMessage: message,
			},
		})
		return
	}

	var undoSnap *charassist.Draft
	if len(res.ChangedFields) > 0 {
		snap := current
		undoSnap = &snap
	}
	newTurns, newCanUndo := store.commitTurn(sessionID, user.ID, charAssistTurn{
		User:      message,
		Assistant: res.Reply,
		Actions:   res.Actions,
	}, true, undoSnap)

	resp := &charAssistResponseData{
		Panel: &charAssistPanelData{
			SessionID: sessionID,
			HasSearch: api.charAssist.HasSearch(),
			Turns:     newTurns,
			CanUndo:   newCanUndo,
		},
	}
	populateChangedOOB(resp, res.Draft, res.ChangedFields, r.Form)
	api.renderAssistResponse(w, resp)
}

func (api *API) assistCharacterExamples(w http.ResponseWriter, r *http.Request) {
	user := ctxstore.GetUser(r.Context())
	if user == nil {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if api.charAssist == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}

	if err := parseAnyForm(r); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	store := api.assistSessionStore()
	sessionID, _ := store.getOrCreate(strings.TrimSpace(r.Form.Get("session_id")), user.ID)
	_, turns, canUndo := store.snapshotState(sessionID, user.ID)

	current := formToDraft(r.Form)
	clean := current.Clean()

	ctx, cancel := context.WithTimeout(r.Context(), assistRequestTimeout)
	defer cancel()

	added, err := api.charAssist.GenerateExamples(ctx, clean, assistDefaultExamplesCt)
	if err != nil {
		errMsg := "Could not generate examples. Try again."
		if errors.Is(err, charassist.ErrEmptyInput) {
			errMsg = "Add a name or description before generating examples."
		} else {
			api.logger.Error("character assist examples failed", "user", user.TwitchLogin, "err", err)
		}
		api.renderAssistResponse(w, &charAssistResponseData{
			Panel: &charAssistPanelData{
				SessionID: sessionID,
				HasSearch: api.charAssist.HasSearch(),
				Turns:     turns,
				CanUndo:   canUndo,
				Error:     errMsg,
			},
		})
		return
	}

	updated := clean
	updated.MessageExamples = append(append([]db.MessageExample(nil), clean.MessageExamples...), added...)

	snap := current
	newTurns, newCanUndo := store.commitTurn(sessionID, user.ID, charAssistTurn{
		User:      "Add 3 message examples",
		Assistant: fmt.Sprintf("Added %d examples.", len(added)),
		Actions:   []charassist.ToolAction{{Kind: "examples", Label: fmt.Sprintf("added %d examples", len(added))}},
	}, false, &snap)

	resp := &charAssistResponseData{
		Panel: &charAssistPanelData{
			SessionID: sessionID,
			HasSearch: api.charAssist.HasSearch(),
			Turns:     newTurns,
			CanUndo:   newCanUndo,
		},
	}
	populateChangedOOB(resp, updated, map[string]bool{"message_examples": true}, r.Form)
	api.renderAssistResponse(w, resp)
}

func (api *API) assistCharacterUndo(w http.ResponseWriter, r *http.Request) {
	user := ctxstore.GetUser(r.Context())
	if user == nil {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if api.charAssist == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}

	if err := parseAnyForm(r); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	store := api.assistSessionStore()
	sessionID := strings.TrimSpace(r.Form.Get("session_id"))
	prev, turns, canUndo := store.popUndo(sessionID, user.ID)

	resp := &charAssistResponseData{
		Panel: &charAssistPanelData{
			SessionID: sessionID,
			HasSearch: api.charAssist.HasSearch(),
			Turns:     turns,
			CanUndo:   canUndo,
		},
	}
	if prev != nil {
		allFields := map[string]bool{
			"char_name":        true,
			"char_description": true,
			"name":             true,
			"first_message":    true,
			"personality":      true,
			"description":      true,
			"message_examples": true,
		}
		populateChangedOOB(resp, *prev, allFields, r.Form)
	}
	api.renderAssistResponse(w, resp)
}

func (api *API) assistCharacterReset(w http.ResponseWriter, r *http.Request) {
	user := ctxstore.GetUser(r.Context())
	if user == nil {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if api.charAssist == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}

	if err := parseAnyForm(r); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	newSessionID := api.assistSessionStore().reset(strings.TrimSpace(r.Form.Get("session_id")), user.ID)
	api.renderAssistResponse(w, &charAssistResponseData{
		Panel: &charAssistPanelData{
			SessionID: newSessionID,
			HasSearch: api.charAssist.HasSearch(),
		},
	})
}

func (api *API) renderAssistResponse(w http.ResponseWriter, data *charAssistResponseData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = html.ExecuteTemplate(w, "char-assist-response", data)
}

func populateChangedOOB(resp *charAssistResponseData, draft charassist.Draft, changed map[string]bool, form url.Values) {
	if len(changed) == 0 {
		return
	}
	if changed["char_name"] {
		resp.CharName = &charFieldData{Value: draft.CharName, OOB: true}
	}
	if changed["char_description"] {
		resp.CharDesc = &charFieldData{Value: draft.CharDescription, OOB: true}
	}
	if changed["name"] {
		resp.Name = &charFieldData{Value: draft.Name, OOB: true}
	}
	if changed["first_message"] {
		resp.FirstMsg = &charFieldData{Value: draft.FirstMessage, OOB: true}
	}
	if changed["personality"] {
		resp.Personality = &charFieldData{Value: draft.Personality, OOB: true}
	}
	if changed["description"] {
		resp.Description = &charFieldData{Value: draft.Description, OOB: true}
	}
	if changed["message_examples"] {
		resp.Examples = &charExamplesData{
			Examples: draftToMsgExamples(draft.MessageExamples),
			OOB:      true,
		}
	}
	resp.SaveStatus = &saveStatus{
		ID:       characterSaveStatusID,
		Baseline: form.Get("_baseline"),
		Skip:     characterSaveSkip,
		Saved:    form.Get("_saved") == "1",
		Check:    true,
		OOB:      true,
	}
}

func draftToMsgExamples(examples []db.MessageExample) *msgExamples {
	out := &msgExamples{ID: 0}
	if len(examples) == 0 {
		out.MessageExamples = []msgExample{{ID: 0}}
		return out
	}
	out.MessageExamples = make([]msgExample, len(examples))
	for i, ex := range examples {
		out.MessageExamples[i] = msgExample{
			ID:       i,
			Request:  ex.Request,
			Response: ex.Response,
		}
	}
	out.ID = len(examples) - 1
	return out
}

func formToDraft(form url.Values) charassist.Draft {
	draft := charassist.Draft{
		CharName:        form.Get("char_name"),
		CharDescription: form.Get("char_description"),
		Name:            form.Get("name"),
		Description:     form.Get("description"),
		Personality:     form.Get("personality"),
		FirstMessage:    form.Get("first_message"),
	}

	type pair struct {
		id       int
		request  string
		response string
	}
	byID := make(map[int]*pair)
	for key, vals := range form {
		if len(vals) == 0 {
			continue
		}
		if strings.HasPrefix(key, requestPrefix) {
			if id, err := strconv.Atoi(key[len(requestPrefix):]); err == nil {
				p := byID[id]
				if p == nil {
					p = &pair{id: id}
					byID[id] = p
				}
				p.request = vals[0]
			}
		} else if strings.HasPrefix(key, responsePrefix) {
			if id, err := strconv.Atoi(key[len(responsePrefix):]); err == nil {
				p := byID[id]
				if p == nil {
					p = &pair{id: id}
					byID[id] = p
				}
				p.response = vals[0]
			}
		}
	}

	ids := make([]int, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	for _, id := range ids {
		p := byID[id]
		draft.MessageExamples = append(draft.MessageExamples, db.MessageExample{
			Request:  p.request,
			Response: p.response,
		})
	}
	return draft
}
