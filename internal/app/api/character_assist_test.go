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
	chatResult  *charassist.ChatResult
	examples    []db.MessageExample
	hasSearch   bool
	err         error
	lastMsg     string
	lastHistory []charassist.ChatMessage
	lastCurrent charassist.Draft
	lastCount   int
}

func (s *stubCharAssistant) Chat(_ context.Context, history []charassist.ChatMessage, userMessage string, current charassist.Draft) (*charassist.ChatResult, error) {
	s.lastHistory = history
	s.lastMsg = userMessage
	s.lastCurrent = current
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
	assert.Equal(t, "Original bio", stub.lastCurrent.Description)

	assert.Contains(t, body, `id="char_assist_panel"`)
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

func TestCharacterTemplate_SidePanelVisibility(t *testing.T) {
	withAssist := getString("character.html", &characterPage{
		MessageExamples: &msgExamples{},
		Assist:          &charAssistPanelData{SessionID: "s1", HasSearch: true},
	})
	assert.Contains(t, withAssist, `id="char_assist_panel"`)
	assert.Contains(t, withAssist, `hx-post="/characters/assist/chat"`)
	assert.Contains(t, withAssist, `hx-post="/characters/assist/examples"`)
	assert.NotContains(t, withAssist, "<script")

	withoutAssist := getString("character.html", &characterPage{
		MessageExamples: &msgExamples{},
	})
	assert.NotContains(t, withoutAssist, `id="char_assist_panel"`)
	assert.NotContains(t, withoutAssist, `hx-post="/characters/assist/examples"`)
}
