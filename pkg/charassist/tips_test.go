package charassist

import (
	"context"
	"encoding/json"
	"testing"

	"app/db"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestContextualTips_EmptyDraft(t *testing.T) {
	tips := ContextualTips(Draft{}, true)
	require.Len(t, tips, 4)
	assert.Equal(t, "Real person / streamer", tips[0].Label)
	assert.Contains(t, tips[0].Prompt, "Research")

	noSearch := ContextualTips(Draft{}, false)
	assert.NotContains(t, noSearch[0].Prompt, "Research")
}

func TestContextualTips_PartialAndArtifacts(t *testing.T) {
	draft := Draft{
		Name:        "Bane",
		Description: "masked mastermind",
		MessageExamples: []db.MessageExample{
			{Request: "Hi", Response: "*adjusts mask* Hello there."},
		},
	}
	tips := ContextualTips(draft, true)
	require.Len(t, tips, 4)
	labels := []string{tips[0].Label, tips[1].Label, tips[2].Label, tips[3].Label}
	assert.Contains(t, labels, "Fill missing fields")
	assert.Contains(t, labels, "Expand description")
	assert.Contains(t, labels, "Clean examples for TTS")
	assert.Contains(t, labels, "Add 3 examples")
	assert.Contains(t, tips[0].Prompt, "Bane")
}

func TestContextualTips_CompleteCard(t *testing.T) {
	draft := Draft{
		CharName:        "Forsen",
		CharDescription: "God gamer.",
		Name:            "Forsen",
		Description:     "Forsen is a popular Swedish Twitch livestreamer known for his dry sarcasm, deadpan humor, terrible posture, stubborn Minecraft speedruns, and claiming every loss is just unlucky.",
		Personality:     "sarcastic, confident, phlegmatic",
		FirstMessage:    "Ask your stupid question.",
		MessageExamples: []db.MessageExample{
			{Request: "Q1", Response: "A1"},
			{Request: "Q2", Response: "A2"},
			{Request: "Q3", Response: "A3"},
			{Request: "Q4", Response: "A4"},
		},
	}
	tips := ContextualTips(draft, true)
	require.Len(t, tips, 4)
	assert.Equal(t, "Search recent lore", tips[0].Label)
	assert.Contains(t, tips[0].Prompt, "Forsen")
	assert.Equal(t, "Add off-topic examples", tips[1].Label)
}

func TestGenerateTips_StrictSchema(t *testing.T) {
	stub := &stubChatClient{
		model: "qwen36-hauhau",
		reply: `{
			"tips": [
				{"label": "Roast Dota drafts", "prompt": "Add 2 examples roasting terrible Dota 2 drafts."},
				{"label": "Roast Dota drafts", "prompt": "Duplicate should be dropped."},
				{"label": "Add Arsenal banter", "prompt": "Add an example about Arsenal FC."}
			]
		}`,
	}
	a := New(stub, &stubSearcher{})
	tips, err := a.GenerateTips(context.Background(), Draft{Name: "Aui_2000"}, []ChatMessage{
		{Role: "user", Content: "build Aui_2000"},
		{Role: "assistant", Content: "Built Aui_2000."},
	}, true)
	require.NoError(t, err)
	require.Len(t, tips, 2)
	assert.Equal(t, "Roast Dota drafts", tips[0].Label)
	assert.Equal(t, "Add Arsenal banter", tips[1].Label)

	require.NotNil(t, stub.lastReq)
	assert.Equal(t, tipsMaxTokens, stub.lastReq.MaxTokens)
	assert.Equal(t, false, stub.lastReq.ChatTemplateKwargs["enable_thinking"])
	require.NotNil(t, stub.lastReq.ResponseFormat)
	assert.Equal(t, "json_schema", stub.lastReq.ResponseFormat.Type)
	require.NotNil(t, stub.lastReq.ResponseFormat.JSONSchema)
	assert.True(t, stub.lastReq.ResponseFormat.JSONSchema.Strict)
	assert.True(t, json.Valid(stub.lastReq.ResponseFormat.JSONSchema.Schema))
	assert.Contains(t, stub.lastReq.Messages[1].StrContent, "Aui_2000")
	assert.Contains(t, stub.lastReq.Messages[1].StrContent, "Web search is enabled")
}

func TestGenerateTips_EmptyDraftSkipsLLM(t *testing.T) {
	stub := &stubChatClient{model: "qwen36-hauhau"}
	a := New(stub, &stubSearcher{})
	tips, err := a.GenerateTips(context.Background(), Draft{}, nil, true)
	require.NoError(t, err)
	require.Len(t, tips, 4)
	assert.Nil(t, stub.lastReq, "blank draft uses instant starter tips without calling LLM")
}
