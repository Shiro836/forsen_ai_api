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

// Planner handles the planning of agentic conversations
type Planner struct {
	client *llm.Client
}

// InitialPlan represents the plan for the first turn of the conversation
type InitialPlan struct {
	FirstSpeakerName string `json:"first_speaker_name"`
	FirstMessageText string `json:"first_message_text"`
}

// NextSpeaker represents the decision for the next turn
type NextSpeaker struct {
	NextSpeakerName string `json:"next_speaker_name"`
}

// NewPlanner creates a new conversation planner
func NewPlanner(client *llm.Client) *Planner {
	return &Planner{
		client: client,
	}
}

// PlanInitialTurn determines who starts the conversation and what they say based on the prompt
func (p *Planner) PlanInitialTurn(ctx context.Context, prompt string, cards []*db.Card) (*InitialPlan, error) {
	if len(cards) == 0 {
		return nil, fmt.Errorf("no characters available")
	}

	characterNames := make([]string, len(cards))
	for i, card := range cards {
		characterNames[i] = card.Name
	}

	// Schema for InitialPlan
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

	// Build character context
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
		return nil, fmt.Errorf("failed to parse LLM response: %w", err)
	}

	return &plan, nil
}

func (p *Planner) SelectNextSpeaker(ctx context.Context, prompt string, history []llm.Message, characterNames []string) (string, error) {
	if len(characterNames) == 0 {
		return "END", nil
	}

	var lastSpeakerName string
	if len(history) > 0 {
		lastMessage := history[len(history)-1]
		for _, content := range lastMessage.Content {
			if content.Type == "text" {
				// Format is "CharacterName: message text"
				parts := strings.SplitN(content.Text, ":", 2)
				if len(parts) > 0 {
					lastSpeakerName = strings.TrimSpace(parts[0])
				}
				break
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

	systemPrompt := "You are a conversation director. Analyze the conversation history. Has it reached a natural conclusion? Has it reached unending repeating loop? If there is any sign, however smal it is, then select 'END'. If not, select the character who should speak next to continue the flow naturally."

	messages := append([]llm.Message(nil), history...)
	messages = append(messages, llm.Message{
		Role:    "system",
		Content: []llm.MessageContent{{Type: "text", Text: systemPrompt}},
	})
	messages = append(messages, llm.Message{
		Role:    "user",
		Content: []llm.MessageContent{{Type: "text", Text: fmt.Sprintf("Topic: %s. Who speaks next between following speakers? END is chosen if conversation reached it's dead end or there is some repetition happening. Available: %s, END", prompt, strings.Join(validOptions[:len(validOptions)-1], ", "))}},
	})

	response, err := p.client.AskGuided(ctx, messages, schemaBytes, 0.0)
	if err != nil {
		return "", fmt.Errorf("failed to call LLM: %w", err)
	}

	var result NextSpeaker
	if err := json.Unmarshal([]byte(response), &result); err != nil {
		return "", fmt.Errorf("failed to parse LLM response: %w", err)
	}

	return result.NextSpeakerName, nil
}
