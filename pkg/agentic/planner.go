package agentic

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"app/db"
	"app/pkg/charutil"
	"app/pkg/llm"
)

type Planner struct {
	client *llm.Client
}

type FirstSpeakerPlan struct {
	FirstSpeakerName string `json:"first_speaker_name"`
}

type InitialPlan struct {
	FirstSpeakerName string `json:"first_speaker_name"`
	FirstMessageText string `json:"first_message_text"`
}

type NextSpeaker struct {
	NextSpeakerName string `json:"next_speaker_name"`
}

func NewPlanner(client *llm.Client) *Planner {
	return &Planner{
		client: client,
	}
}

func (p *Planner) SelectFirstSpeaker(ctx context.Context, prompt string, cards []*db.Card) (string, error) {
	if len(cards) == 0 {
		return "", fmt.Errorf("no characters available")
	}

	characterNames := make([]string, len(cards))
	for i, card := range cards {
		characterNames[i] = card.Name
	}

	schema := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"first_speaker_name": map[string]interface{}{
				"type": "string",
				"enum": characterNames,
			},
		},
		"required": []string{"first_speaker_name"},
	}

	schemaBytes, err := json.Marshal(schema)
	if err != nil {
		return "", fmt.Errorf("failed to marshal schema: %w", err)
	}

	charContext := &strings.Builder{}
	for _, card := range cards {
		charContext.WriteString(fmt.Sprintf("\n%s", card.Name))
		charContext.WriteString(fmt.Sprintf("\n%s", charutil.BuildCharacterContext(card.Name, card.Data.Description, card.Data.Personality, card.Data.MessageExamples)))
	}

	systemPrompt := "You are a conversation planner. Your goal is to analyze the user's prompt and decide who should speak first to strictly follow the user's intent."
	userPrompt := fmt.Sprintf("Available Characters:%s\n\nUser Prompt: %q\n\nTask: Choose exactly one character who should speak first.", charContext.String(), prompt)

	messages := []llm.Message{
		{Role: "system", Content: []llm.MessageContent{{Type: "text", Text: systemPrompt}}},
		{Role: "user", Content: []llm.MessageContent{{Type: "text", Text: userPrompt}}},
	}

	response, err := p.client.AskGuided(ctx, messages, schemaBytes, 0.0)
	if err != nil {
		return "", fmt.Errorf("failed to call LLM: %w", err)
	}

	var plan FirstSpeakerPlan
	if err := json.Unmarshal([]byte(response), &plan); err != nil {
		return "", fmt.Errorf("failed to parse LLM response: %w; raw=%q", err, response)
	}

	return plan.FirstSpeakerName, nil
}

func (p *Planner) PlanInitialTurn(ctx context.Context, prompt string, cards []*db.Card) (*InitialPlan, error) {
	if len(cards) == 0 {
		return nil, fmt.Errorf("no characters available")
	}

	characterNames := make([]string, len(cards))
	for i, card := range cards {
		characterNames[i] = card.Name
	}

	schema := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"first_speaker_name": map[string]interface{}{
				"type": "string",
				"enum": characterNames,
			},
			"first_message_text": map[string]interface{}{
				"type": "string",
			},
		},
		"required": []string{"first_speaker_name", "first_message_text"},
	}

	schemaBytes, err := json.Marshal(schema)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal schema: %w", err)
	}

	charContext := &strings.Builder{}
	for _, card := range cards {
		charContext.WriteString(fmt.Sprintf("\n%s", card.Name))
		charContext.WriteString(fmt.Sprintf("\n%s", charutil.BuildCharacterContext(card.Name, card.Data.Description, card.Data.Personality, card.Data.MessageExamples)))
	}

	systemPrompt := "You are a conversation planner. Your goal is to analyze the user's prompt and decide who should speak first and what they should say to strictly follow the user's intent."
	userPrompt := fmt.Sprintf("Available Characters:%s\n\n User Prompt: \"%s\" \n\n Task: Decide the first speaker and write ONLY their message text (without the character name prefix). The message should be in character style and directly follow the user's prompt scenario.", charContext.String(), prompt)

	messages := []llm.Message{
		{Role: "system", Content: []llm.MessageContent{{Type: "text", Text: systemPrompt}}},
		{Role: "user", Content: []llm.MessageContent{{Type: "text", Text: userPrompt}}},
	}

	response, err := p.client.AskGuided(ctx, messages, schemaBytes, 1.0)
	if err != nil {
		return nil, fmt.Errorf("failed to call LLM: %w", err)
	}

	var plan InitialPlan
	if err := json.Unmarshal([]byte(response), &plan); err != nil {
		return nil, fmt.Errorf("failed to parse LLM response: %w; raw=%q", err, response)
	}

	return &plan, nil
}

func (p *Planner) SelectNextSpeaker(ctx context.Context, prompt string, history []llm.Message, characterNames []string) (string, error) {
	if len(characterNames) == 0 {
		return "END", nil
	}

	var lastSpeakerName string
	var turns []string
	if len(history) > 0 {
		for _, msg := range history {
			for _, content := range msg.Content {
				if content.Type != "text" {
					continue
				}
				t := strings.TrimSpace(content.Text)
				if t != "" {
					turns = append(turns, t)
				}
			}
		}

		if len(turns) > 0 {
			parts := strings.SplitN(turns[len(turns)-1], ":", 2)
			if len(parts) > 0 {
				lastSpeakerName = strings.TrimSpace(parts[0])
			}
		}
	}

	validOptions := make([]string, 0, len(characterNames))
	for _, name := range characterNames {
		if !strings.EqualFold(name, lastSpeakerName) {
			validOptions = append(validOptions, name)
		}
	}
	validOptions = append(validOptions, "END")

	schema := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"next_speaker_name": map[string]interface{}{
				"type": "string",
				"enum": validOptions,
			},
		},
		"required": []string{"next_speaker_name"},
	}

	schemaBytes, err := json.Marshal(schema)
	if err != nil {
		return "", fmt.Errorf("failed to marshal schema: %w", err)
	}

	systemPrompt := "You are a conversation director. Your job is to decide who speaks next, or whether the conversation should end."

	if len(turns) > 12 {
		turns = turns[len(turns)-12:]
	}
	transcript := strings.Join(turns, "\n")

	userPrompt := fmt.Sprintf(
		"Topic: %s\n\nConversation so far:\n%s\n\nAvailable next speakers: %s, END\n\nRules:\n"+
			"- Choose END if the conversation has reached a natural conclusion.\n"+
			"- Choose END if the conversation is looping/repeating (even slightly), including rephrasing the same points.\n"+
			"- If uncertain, prefer END over continuing.\n"+
			"- Otherwise pick the single best next speaker to continue naturally.\n",
		prompt,
		transcript,
		strings.Join(validOptions[:len(validOptions)-1], ", "),
	)

	messages := []llm.Message{
		{Role: "system", Content: []llm.MessageContent{{Type: "text", Text: systemPrompt}}},
		{Role: "user", Content: []llm.MessageContent{{Type: "text", Text: userPrompt}}},
	}

	response, err := p.client.AskGuided(ctx, messages, schemaBytes, 0.0)
	if err != nil {
		return "", fmt.Errorf("failed to call LLM: %w", err)
	}

	var result NextSpeaker
	if err := json.Unmarshal([]byte(response), &result); err != nil {
		return "", fmt.Errorf("failed to parse LLM response: %w; raw=%q", err, response)
	}

	return result.NextSpeakerName, nil
}
