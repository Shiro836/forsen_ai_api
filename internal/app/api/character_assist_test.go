package api

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"app/db"
	"app/pkg/charassist"
	"app/pkg/ctxstore"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stubCharAssistant struct {
	chatResult    *charassist.ChatResult
	examples      []db.MessageExample
	tips          []charassist.Tip
	tipsErr       error
	hasSearch     bool
	err           error
	lastMsg       string
	lastHistory   []charassist.ChatMessage
	lastCurrent   charassist.Draft
	lastWebSearch bool
	lastCount     int
}

func (s *stubCharAssistant) Chat(_ context.Context, history []charassist.ChatMessage, userMessage string, current charassist.Draft, webSearch bool) (*charassist.ChatResult, error) {
	s.lastHistory = history
	s.lastMsg = userMessage
	s.lastCurrent = current
	s.lastWebSearch = webSearch
	if s.err != nil {
		return nil, s.err
	}
	return s.chatResult, nil
}

func (s *stubCharAssistant) GenerateExamples(_ context.Context, current charassist.Draft, count int) ([]db.MessageExample, error) {
	s.lastCurrent = current
	s.lastCount = count
	if s.err != nil {
		return nil, s.err
	}
	return s.examples, nil
}

func (s *stubCharAssistant) GenerateTips(_ context.Context, current charassist.Draft, history []charassist.ChatMessage, webSearch bool) ([]charassist.Tip, error) {
	s.lastCurrent = current
	s.lastHistory = history
	s.lastWebSearch = webSearch
	if s.tipsErr != nil {
		return nil, s.tipsErr
	}
	return s.tips, nil
}

func (s *stubCharAssistant) HasSearch() bool { return s.hasSearch }

func authedFormReq(user *db.User, target string, form url.Values) *http.Request {
	r := httptest.NewRequest(http.MethodPost, target, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return r.WithContext(ctxstore.WithUser(r.Context(), user))
}

func TestAssistCharacterChat_OOBFieldsAndUndo(t *testing.T) {
	user := &db.User{ID: uuid.New(), TwitchLogin: "forsen"}
	stub := &stubCharAssistant{
		hasSearch: true,
		chatResult: &charassist.ChatResult{
			Reply: "Updated description and examples.",
			Draft: charassist.Draft{
				CharName:    "Forsen",
				Name:        "Forsen",
				Description: "Swedish streamer <script>alert(1)</script>.",
				MessageExamples: []db.MessageExample{
					{Request: "Sub 15?", Response: "Unlucky seed."},
				},
			},
			ChangedFields: map[string]bool{
				"description":      true,
				"message_examples": true,
			},
			Actions: []charassist.ToolAction{
				{Kind: "search", Label: "searched: forsen"},
				{Kind: "edit", Label: "updated description"},
			},
		},
	}
	a := &API{
		logger:             slog.New(slog.NewTextHandler(io.Discard, nil)),
		charAssist:         stub,
		charAssistSessions: newCharAssistSessionStore(),
	}

	sid := uuid.New().String()
	form := url.Values{
		"session_id":  {sid},
		"message":     {"update forsen bio"},
		"web_search":  {"1"},
		"char_name":   {"Forsen"},
		"name":        {"Forsen"},
		"description": {"Original bio"},
		"request_0":   {"Old Q"},
		"response_0":  {"Old A"},
		"_baseline":   {"abc123"},
	}
	w := httptest.NewRecorder()
	a.assistCharacterChat(w, authedFormReq(user, "/characters/assist/chat", form))

	require.Equal(t, http.StatusOK, w.Code)
	body := w.Body.String()
	assert.Equal(t, "update forsen bio", stub.lastMsg)
	assert.True(t, stub.lastWebSearch)
	assert.Equal(t, "Original bio", stub.lastCurrent.Description)

	assert.Contains(t, body, `id="char_assist_panel"`)
	assert.Contains(t, body, `id="char_assist_tips"`)
	assert.Contains(t, body, `hx-post="/characters/assist/tips"`, "chat response triggers async tips refresh")
	assert.Contains(t, body, `hx-get="/characters/assist/prompt?text=`, "prompt tips stay visible after turns")
	assert.Contains(t, body, `name="web_search" value="1" class="peer sr-only" checked`)
	assert.Contains(t, body, "searched: forsen")
	assert.Contains(t, body, "Undo")
	assert.Contains(t, body, `id="description"`)
	assert.Contains(t, body, `hx-swap-oob="outerHTML"`)
	assert.Contains(t, body, `&lt;script&gt;alert(1)&lt;/script&gt;`)
	assert.NotContains(t, body, `<script>alert(1)</script>`)
	assert.Contains(t, body, `id="message_examples_container"`)
	assert.Contains(t, body, `id="character_save_result"`)
	assert.Contains(t, body, `value="abc123"`)
	assert.NotContains(t, body, `id="char_name"`, "unchanged field must not be swapped OOB")

	// Undo restores the pre-edit snapshot via OOB swaps.
	wUndo := httptest.NewRecorder()
	a.assistCharacterUndo(wUndo, authedFormReq(user, "/characters/assist/undo", url.Values{
		"session_id": {sid},
		"_baseline":  {"abc123"},
	}))
	require.Equal(t, http.StatusOK, wUndo.Code)
	undoBody := wUndo.Body.String()
	assert.Contains(t, undoBody, "Original bio")
	assert.Contains(t, undoBody, "Old Q")
	assert.Contains(t, undoBody, `id="character_save_result"`)
	assert.NotContains(t, undoBody, `hx-post="/characters/assist/undo"`, "undo stack is now empty")
}

func TestAssistCharacterChat_ErrorKeepsDraftMessage(t *testing.T) {
	user := &db.User{ID: uuid.New(), TwitchLogin: "forsen"}
	stub := &stubCharAssistant{err: errors.New("upstream timeout")}
	a := &API{
		logger:             slog.New(slog.NewTextHandler(io.Discard, nil)),
		charAssist:         stub,
		charAssistSessions: newCharAssistSessionStore(),
	}

	w := httptest.NewRecorder()
	a.assistCharacterChat(w, authedFormReq(user, "/characters/assist/chat", url.Values{
		"session_id": {uuid.New().String()},
		"message":    {"keep this prompt"},
	}))
	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "keep this prompt")
	assert.Contains(t, w.Body.String(), "Something went wrong")
	assert.NotContains(t, w.Body.String(), "upstream timeout")
}

func TestAssistCharacterExamples_OOBAppendsAndDropsBlankRows(t *testing.T) {
	user := &db.User{ID: uuid.New(), TwitchLogin: "forsen"}
	stub := &stubCharAssistant{
		examples: []db.MessageExample{
			{Request: "New Q1", Response: "New A1"},
			{Request: "New Q2", Response: "New A2"},
		},
	}
	a := &API{
		logger:             slog.New(slog.NewTextHandler(io.Discard, nil)),
		charAssist:         stub,
		charAssistSessions: newCharAssistSessionStore(),
	}

	w := httptest.NewRecorder()
	a.assistCharacterExamples(w, authedFormReq(user, "/characters/assist/examples", url.Values{
		"session_id":  {uuid.New().String()},
		"name":        {"Forsen"},
		"description": {"Swedish streamer"},
		"request_0":   {""},
		"response_0":  {""},
		"request_1":   {"Old Q"},
		"response_1":  {"Old A"},
		"_baseline":   {"base1"},
	}))

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, 3, stub.lastCount)
	require.Len(t, stub.lastCurrent.MessageExamples, 1, "blank rows stripped before calling assistant")

	body := w.Body.String()
	assert.Contains(t, body, `name="request_0"`)
	assert.Contains(t, body, `Old Q`)
	assert.Contains(t, body, `name="request_2"`)
	assert.Contains(t, body, `New Q2`)
	assert.Contains(t, body, `hx-get="/new_message_example/2"`)
	assert.Contains(t, body, `id="character_save_result"`)
}

func TestAssistCharacterPromptTipsAndSearchToggle(t *testing.T) {
	user := &db.User{ID: uuid.New(), TwitchLogin: "forsen"}
	stub := &stubCharAssistant{
		hasSearch:  true,
		chatResult: &charassist.ChatResult{Reply: "ok"},
	}
	a := &API{
		logger:             slog.New(slog.NewTextHandler(io.Discard, nil)),
		charAssist:         stub,
		charAssistSessions: newCharAssistSessionStore(),
	}

	// Prefill endpoint only returns the composer textarea and never invokes Chat.
	r := httptest.NewRequest(http.MethodGet, "/characters/assist/prompt?text=Fill+empty+fields", nil)
	r = r.WithContext(ctxstore.WithUser(r.Context(), user))
	w := httptest.NewRecorder()
	a.assistCharacterPrompt(w, r)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `id="char_assist_message"`)
	assert.Contains(t, w.Body.String(), "Fill empty fields")
	assert.Contains(t, w.Body.String(), "autofocus")
	assert.Empty(t, stub.lastMsg)

	// Unchecked web_search disables search for the turn and preserves unchecked state.
	wChat := httptest.NewRecorder()
	a.assistCharacterChat(wChat, authedFormReq(user, "/characters/assist/chat", url.Values{
		"session_id": {uuid.New().String()},
		"message":    {"tweak tone"},
	}))
	require.Equal(t, http.StatusOK, wChat.Code)
	assert.False(t, stub.lastWebSearch)
	assert.NotContains(t, wChat.Body.String(), `name="web_search" value="1" class="peer sr-only" checked`)

	// Async tips endpoint renders strict-schema tips with Async disabled (no loop).
	stub.tips = []charassist.Tip{{Label: "Add Minecraft memes", Prompt: "Add 2 Minecraft examples."}}
	wTips := httptest.NewRecorder()
	a.assistCharacterTips(wTips, authedFormReq(user, "/characters/assist/tips", url.Values{
		"session_id": {uuid.New().String()},
		"name":       {"Forsen"},
		"web_search": {"1"},
	}))
	require.Equal(t, http.StatusOK, wTips.Code)
	assert.True(t, stub.lastWebSearch)
	assert.Contains(t, wTips.Body.String(), "Add Minecraft memes")
	assert.NotContains(t, wTips.Body.String(), `hx-trigger="load"`, "rendered tips must not re-trigger load")
}

func TestCharacterTemplate_SidePanelVisibility(t *testing.T) {
	withAssist := getString("character.html", &characterPage{
		MessageExamples: &msgExamples{},
		Assist: &charAssistPanelData{
			SessionID: "s1",
			HasSearch: true,
			SearchOn:  true,
			Tips:      charassist.ContextualTips(charassist.Draft{}, true),
			AsyncTips: false,
		},
	})
	assert.Contains(t, withAssist, `id="char_assist_panel"`)
	assert.Contains(t, withAssist, `hx-post="/characters/assist/chat"`)
	assert.Contains(t, withAssist, `hx-get="/characters/assist/prompt?text=`)
	assert.Contains(t, withAssist, "Real person / streamer")
	assert.NotContains(t, withAssist, `hx-post="/characters/assist/tips"`, "blank draft does not fire async tips")
	assert.Contains(t, withAssist, `name="web_search" value="1" class="peer sr-only" checked`)
	assert.Contains(t, withAssist, `hx-post="/characters/assist/examples"`)
	assert.NotContains(t, withAssist, "<script")

	existingChar := getString("character.html", &characterPage{
		MessageExamples: &msgExamples{},
		Assist: &charAssistPanelData{
			SessionID: "s2",
			HasSearch: true,
			SearchOn:  true,
			Tips:      charassist.ContextualTips(charassist.Draft{Name: "Forsen"}, true),
			AsyncTips: true,
		},
	})
	assert.Contains(t, existingChar, `hx-post="/characters/assist/tips"`, "existing character loads async AI tips")
	assert.Contains(t, existingChar, `hx-trigger="load"`)

	withoutAssist := getString("character.html", &characterPage{
		MessageExamples: &msgExamples{},
	})
	assert.NotContains(t, withoutAssist, `id="char_assist_panel"`)
	assert.NotContains(t, withoutAssist, `hx-post="/characters/assist/examples"`)
}

func TestAssistSecurityHardening(t *testing.T) {
	store := newCharAssistSessionStore()
	u1 := uuid.New()
	u2 := uuid.New()

	// Non-UUID session IDs are rejected and replaced with a fresh UUID.
	gotID, _ := store.getOrCreate(`evil", "x": "1`, u1)
	_, err := uuid.Parse(gotID)
	require.NoError(t, err)
	assert.NotContains(t, gotID, "evil")

	// Cross-user session isolation: u2 cannot read, mutate, or undo u1's session.
	store.commitTurn(gotID, u1, charAssistTurn{User: "hi", Assistant: "secret"}, true, &charassist.Draft{Name: "orig"})
	hist, turns, canUndo := store.snapshotState(gotID, u2)
	assert.Empty(t, hist)
	assert.Empty(t, turns)
	assert.False(t, canUndo)

	turns2, _ := store.commitTurn(gotID, u2, charAssistTurn{User: "hijack", Assistant: "no"}, true, nil)
	assert.Empty(t, turns2)
	_, turns1, canUndo1 := store.snapshotState(gotID, u1)
	require.Len(t, turns1, 1)
	assert.Equal(t, "secret", turns1[0].Assistant)
	assert.True(t, canUndo1)

	// Session store enforces hard cap via eviction.
	for i := 0; i < assistMaxSessionsTotal+25; i++ {
		store.getOrCreate("", u1)
	}
	assert.LessOrEqual(t, len(store.sessions), assistMaxSessionsTotal)
}
