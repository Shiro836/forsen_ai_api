package charassist

import (
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

func TestParseTips(t *testing.T) {
	raw := "Updated the bio and added 4 examples.\n\n" +
		"[[tip: Roast Dota builds | Add 2 examples roasting terrible Dota 2 builds.]]\n" +
		"- [[tip: More TI quotes | Search for funny TI 2025 interviews and weave quotes into the bio.]]\n" +
		"[[tip: Roast Dota builds | Duplicate ignored]]\n"

	clean, tips := ParseTips(raw)
	assert.Equal(t, "Updated the bio and added 4 examples.", clean)
	require.Len(t, tips, 2)
	assert.Equal(t, "Roast Dota builds", tips[0].Label)
	assert.Equal(t, "Add 2 examples roasting terrible Dota 2 builds.", tips[0].Prompt)
	assert.Equal(t, "More TI quotes", tips[1].Label)
}
