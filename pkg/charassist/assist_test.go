package charassist

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"app/db"
	"app/pkg/llm"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type stubChatClient struct {
	model   string
	reply   string
	err     error
	lastReq *llm.ChatRequest
}

func (s *stubChatClient) ReqChat(_ context.Context, req *llm.ChatRequest) (*llm.ChatResponse, error) {
	s.lastReq = req
	if s.err != nil {
		return nil, s.err
	}
	return &llm.ChatResponse{
		Model: s.model,
		Choices: []llm.ChatResponseChoice{
			{Message: llm.ChatResponseMessage{Content: s.reply}, FinishReason: "stop"},
		},
	}, nil
}

func (s *stubChatClient) GetModel() string { return s.model }

func TestGenerate_FromScratch(t *testing.T) {
	reply := `{
		"char_name": "  Gordon Ramsay ",
		"char_description": "Fiery chef who roasts your cooking.",
		"name": "",
		"description": "World-famous British chef with a volcanic temper.",
		"personality": "fiery, perfectionist, sarcastic",
		"first_message": "What disaster did you bring me today?",
		"message_examples": [
			{"request": "I burned water", "response": "How do you burn water, you donkey?"},
			{"request": "", "response": "ignored incomplete"},
			{"request": "Rate my pasta", "response": "It is raw and bland."}
		]
	}`
	stub := &stubChatClient{model: "qwen36-hauhau", reply: reply}
	a := New(stub, nil)

	draft, err := a.Generate(context.Background(), "Gordon Ramsay", Draft{})
	require.NoError(t, err)

	assert.Equal(t, "Gordon Ramsay", draft.CharName)
	assert.Equal(t, "Gordon Ramsay", draft.Name, "empty name falls back to char_name")
	assert.Equal(t, "Fiery chef who roasts your cooking.", draft.CharDescription)
	assert.Equal(t, "World-famous British chef with a volcanic temper.", draft.Description)
	assert.Equal(t, "fiery, perfectionist, sarcastic", draft.Personality)
	assert.Equal(t, "What disaster did you bring me today?", draft.FirstMessage)
	require.Len(t, draft.MessageExamples, 2)
	assert.Equal(t, "I burned water", draft.MessageExamples[0].Request)

	require.NotNil(t, stub.lastReq)
	assert.Equal(t, "qwen36-hauhau", stub.lastReq.Model)
	assert.Equal(t, cardMaxTokens, stub.lastReq.MaxTokens)
	assert.Equal(t, false, stub.lastReq.ChatTemplateKwargs["enable_thinking"])
	require.NotNil(t, stub.lastReq.ResponseFormat)
	assert.Equal(t, "json_schema", stub.lastReq.ResponseFormat.Type)
	require.NotNil(t, stub.lastReq.ResponseFormat.JSONSchema)
	assert.True(t, stub.lastReq.ResponseFormat.JSONSchema.Strict)
	assert.True(t, json.Valid(stub.lastReq.ResponseFormat.JSONSchema.Schema))
	require.Len(t, stub.lastReq.Messages, 2)
	assert.Contains(t, stub.lastReq.Messages[1].StrContent, "Gordon Ramsay")
	assert.NotContains(t, stub.lastReq.Messages[1].StrContent, "Current character card draft")
}

func TestGenerate_RefinesExistingDraft(t *testing.T) {
	stub := &stubChatClient{
		model: "qwen36-hauhau",
		reply: "```json\n" + `{
			"char_name": "Forsen",
			"char_description": "God gamer.",
			"name": "Forsen",
			"description": "Swedish streamer who keeps dying to lava in Minecraft.",
			"personality": "sarcastic, stubborn",
			"first_message": "Ask your stupid Minecraft question.",
			"message_examples": [
				{"request": "Sub 15 when?", "response": "Tomorrow, obviously. Unlucky seed today."}
			]
		}` + "\n```",
	}
	a := New(stub, nil)

	current := Draft{
		CharName:    "Forsen",
		Name:        "Forsen",
		Description: "Swedish Twitch streamer.",
	}
	draft, err := a.Generate(context.Background(), "add Minecraft speedrunning obsession", current)
	require.NoError(t, err)
	assert.Contains(t, draft.Description, "Minecraft")

	userPrompt := stub.lastReq.Messages[1].StrContent
	assert.Contains(t, userPrompt, "Current character card draft")
	assert.Contains(t, userPrompt, "Swedish Twitch streamer.")
	assert.Contains(t, userPrompt, "add Minecraft speedrunning obsession")
}

func TestGenerate_FillsPartialDraftWithoutPrompt(t *testing.T) {
	stub := &stubChatClient{
		model: "qwen36-hauhau",
		reply: `{
			"char_name": "Okayeg",
			"char_description": "Angry dota teammate.",
			"name": "Okayeg",
			"description": "Always yelling about bad teammates.",
			"personality": "angry, logical",
			"first_message": "Ask your stupid question.",
			"message_examples": [
				{"request": "hi", "response": "when noob say hi, big problem."}
			]
		}`,
	}
	a := New(stub, nil)

	draft, err := a.Generate(context.Background(), "   ", Draft{CharName: "Okayeg"})
	require.NoError(t, err)
	assert.Equal(t, "Okayeg", draft.CharName)
	assert.Contains(t, stub.lastReq.Messages[1].StrContent, "fill in any empty or sparse fields")
}

func TestGenerate_ValidationAndErrors(t *testing.T) {
	a := New(&stubChatClient{model: "qwen36-hauhau"}, nil)

	_, err := a.Generate(context.Background(), "  ", Draft{})
	require.ErrorIs(t, err, ErrEmptyInput)

	aErr := New(&stubChatClient{model: "qwen36-hauhau", err: errors.New("connection refused")}, nil)
	_, err = aErr.Generate(context.Background(), "Forsen", Draft{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "connection refused")

	aBad := New(&stubChatClient{model: "qwen36-hauhau", reply: "not json"}, nil)
	_, err = aBad.Generate(context.Background(), "Forsen", Draft{})
	require.Error(t, err)
}

func TestGenerateExamples(t *testing.T) {
	stub := &stubChatClient{
		model: "qwen36-hauhau",
		reply: `{
			"examples": [
				{"request": "Favorite pizza?", "response": "Pepperoni, obviously."},
				{"request": "", "response": "dropped"},
				{"request": "Why are you late?", "response": "Traffic was unlucky."}
			]
		}`,
	}
	a := New(stub, nil)

	current := Draft{
		Name:        "Forsen",
		Description: "Swedish streamer.",
		Personality: "sarcastic",
		MessageExamples: []db.MessageExample{
			{Request: "Should I wash my legs?", Response: "Who washes their legs?"},
		},
	}

	got, err := a.GenerateExamples(context.Background(), current, 3)
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, "Favorite pizza?", got[0].Request)
	assert.Equal(t, "Why are you late?", got[1].Request)

	require.NotNil(t, stub.lastReq)
	assert.Equal(t, examplesMaxTokens, stub.lastReq.MaxTokens)
	assert.True(t, strings.Contains(stub.lastReq.Messages[1].StrContent, "Should I wash my legs?"))
	assert.True(t, strings.Contains(stub.lastReq.Messages[1].StrContent, "Generate exactly 3 new"))
}

func TestGenerateExamples_EmptyDraftRejected(t *testing.T) {
	a := New(&stubChatClient{model: "qwen36-hauhau"}, nil)
	_, err := a.GenerateExamples(context.Background(), Draft{}, 3)
	require.ErrorIs(t, err, ErrEmptyInput)
}
