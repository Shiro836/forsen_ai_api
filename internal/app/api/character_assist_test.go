package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
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
	draft       *charassist.Draft
	examples    []db.MessageExample
	err         error
	lastPrompt  string
	lastCurrent charassist.Draft
	lastCount   int
}

func (s *stubCharAssistant) Generate(_ context.Context, prompt string, current charassist.Draft) (*charassist.Draft, error) {
	s.lastPrompt = prompt
	s.lastCurrent = current
	if s.err != nil {
		return nil, s.err
	}
	return s.draft, nil
}

func (s *stubCharAssistant) GenerateExamples(_ context.Context, current charassist.Draft, count int) ([]db.MessageExample, error) {
	s.lastCurrent = current
	s.lastCount = count
	if s.err != nil {
		return nil, s.err
	}
	return s.examples, nil
}

func authedReq(method, target, body string) *http.Request {
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	u := &db.User{ID: uuid.New(), TwitchLogin: "forsen"}
	return r.WithContext(ctxstore.WithUser(r.Context(), u))
}

func TestAssistCharacter_Success(t *testing.T) {
	stub := &stubCharAssistant{
		draft: &charassist.Draft{
			CharName:        "Forsen",
			CharDescription: "God gamer.",
			Name:            "Forsen",
			Description:     "Swedish streamer.",
			Personality:     "sarcastic, confident",
			FirstMessage:    "Ask your stupid question.",
			MessageExamples: []db.MessageExample{
				{Request: "Sub 15 <script>?", Response: "Unlucky seed."},
				{Request: "Wash legs?", Response: "Who washes their legs?"},
			},
		},
	}
	a := &API{logger: slog.New(slog.NewTextHandler(io.Discard, nil)), charAssist: stub}

	w := httptest.NewRecorder()
	a.assistCharacter(w, authedReq(http.MethodPost, "/characters/assist", `{"prompt":"Forsen"}`))

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "Forsen", stub.lastPrompt)

	var resp assistCharacterResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "Forsen", resp.CharName)
	require.Len(t, resp.MessageExamples, 2)
	assert.Contains(t, resp.ExamplesHTML, `name="request_0"`)
	assert.Contains(t, resp.ExamplesHTML, `name="response_1"`)
	assert.Contains(t, resp.ExamplesHTML, `hx-get="/new_message_example/1"`)
	assert.NotContains(t, resp.ExamplesHTML, "<script>", "template must escape generated content")
	assert.Contains(t, resp.ExamplesHTML, "&lt;script&gt;")
}

func TestAssistCharacter_Errors(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	t.Run("unauthorized", func(t *testing.T) {
		a := &API{logger: logger, charAssist: &stubCharAssistant{}}
		r := httptest.NewRequest(http.MethodPost, "/characters/assist", strings.NewReader(`{"prompt":"x"}`))
		w := httptest.NewRecorder()
		a.assistCharacter(w, r)
		assert.Equal(t, http.StatusUnauthorized, w.Code)
	})

	t.Run("unconfigured", func(t *testing.T) {
		a := &API{logger: logger}
		w := httptest.NewRecorder()
		a.assistCharacter(w, authedReq(http.MethodPost, "/characters/assist", `{"prompt":"x"}`))
		assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	})

	t.Run("empty input", func(t *testing.T) {
		a := &API{logger: logger, charAssist: &stubCharAssistant{err: charassist.ErrEmptyInput}}
		w := httptest.NewRecorder()
		a.assistCharacter(w, authedReq(http.MethodPost, "/characters/assist", `{}`))
		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("upstream failure", func(t *testing.T) {
		a := &API{logger: logger, charAssist: &stubCharAssistant{err: errors.New("timeout")}}
		w := httptest.NewRecorder()
		a.assistCharacter(w, authedReq(http.MethodPost, "/characters/assist", `{"prompt":"Forsen"}`))
		assert.Equal(t, http.StatusBadGateway, w.Code)
		assert.NotContains(t, w.Body.String(), "timeout")
	})
}

func TestAssistCharacterExamples_AppendsAndDropsBlankRows(t *testing.T) {
	stub := &stubCharAssistant{
		examples: []db.MessageExample{
			{Request: "New Q1", Response: "New A1"},
			{Request: "New Q2", Response: "New A2"},
		},
	}
	a := &API{logger: slog.New(slog.NewTextHandler(io.Discard, nil)), charAssist: stub}

	body := `{
		"name": "Forsen",
		"description": "Swedish streamer",
		"message_examples": [
			{"request": "", "response": ""},
			{"request": "Old Q", "response": "Old A"}
		]
	}`
	w := httptest.NewRecorder()
	a.assistCharacterExamples(w, authedReq(http.MethodPost, "/characters/assist/examples", body))

	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, 3, stub.lastCount)
	require.Len(t, stub.lastCurrent.MessageExamples, 1, "blank rows stripped before calling assistant")

	var resp assistExamplesResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, 2, resp.Added)
	require.Len(t, resp.MessageExamples, 3)
	assert.Equal(t, "Old Q", resp.MessageExamples[0].Request)
	assert.Equal(t, "New Q2", resp.MessageExamples[2].Request)
	assert.Contains(t, resp.ExamplesHTML, `name="request_2"`)
	assert.Contains(t, resp.ExamplesHTML, `hx-get="/new_message_example/2"`)
}

func TestCharacterTemplate_AIAssistVisibility(t *testing.T) {
	withAssist := getString("character.html", &characterPage{
		MessageExamples: &msgExamples{},
		ShowAIAssist:    true,
	})
	assert.Contains(t, withAssist, `id="ai_assist_prompt"`)
	assert.Contains(t, withAssist, `id="ai_assist_btn"`)
	assert.Contains(t, withAssist, `id="ai_assist_examples_btn"`)
	assert.Contains(t, withAssist, `id="message_examples_container"`)

	withoutAssist := getString("character.html", &characterPage{
		MessageExamples: &msgExamples{},
		ShowAIAssist:    false,
	})
	assert.NotContains(t, withoutAssist, `id="ai_assist_prompt"`)
	assert.NotContains(t, withoutAssist, `id="ai_assist_examples_btn"`)
}
