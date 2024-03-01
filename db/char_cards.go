package db

import "context"

type Card struct {
	ID int

	OwnerUserID int

	Data *CardData
}

type CardData struct {
	Name                    string `json:"name"`
	Description             string `json:"description"`
	Personality             string `json:"personality"`
	FirstMessage            string `json:"first_message"`
	MessageExample          string `json:"message_example"`
	Scenario                string `json:"scenario"`
	SystemPrompt            string `json:"system_prompt"`
	PostHistoryInstructions string `json:"post_history_instructions"`
}

func (db *DB) GetCharCard(ctx context.Context, userID int, twitchRewardID string) (*Card, error) {
	panic("not implemented")
}
