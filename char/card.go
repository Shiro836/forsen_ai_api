package char

import (
	"encoding/json"
	"fmt"
)

type Card struct {
	Name                    string `json:"name"`
	Description             string `json:"description"`
	Personality             string `json:"personality"`
	FirstMessage            string `json:"first_message"`
	MessageExample          string `json:"message_example"`
	Scenario                string `json:"scenario"`
	SystemPrompt            string `json:"system_prompt"`
	PostHistoryInstructions string `json:"post_history_instructions"`
}

func FromJson(data []byte) (*Card, error) {
	var card *Card

	if err := json.Unmarshal(data, &card); err != nil {
		return nil, fmt.Errorf("failed to unmarshal character card: %w", err)
	}

	return card, nil
}

func (c *Card) ToJson() ([]byte, error) {
	if data, err := json.Marshal(c); err != nil {
		return nil, fmt.Errorf("failed to marshal character card: %w", err)
	} else {
		return data, nil
	}
}
