package db

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

type TwitchRewardType int

const (
	TwitchRewardTTS TwitchRewardType = iota
	TwitchRewardAI
)

func (t TwitchRewardType) String() string {
	switch t {
	case TwitchRewardTTS:
		return "TTS"
	case TwitchRewardAI:
		return "AI"
	default:
		return "unknown"
	}
}

type TwitchRewardData struct{}

type TwitchReward struct {
	ID uuid.UUID `json:"id"`

	UserID uuid.UUID `json:"user_id"`
	CardID uuid.UUID `json:"card_id"`

	TwitchRewardID string `json:"twitch_reward_id"`

	RewardType TwitchRewardType `json:"reward_type"`

	Data *TwitchRewardData `json:"data"`
}

func (db *DB) UpsertTwitchReward(ctx context.Context, userID uuid.UUID, CardID uuid.UUID, twitchRewardID string, rewardType TwitchRewardType) error {
	_, err := db.Exec(ctx, `
		INSERT INTO reward_buttons (
			user_id,
			card_id,
			twitch_reward_id,
			reward_type
		)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (user_id, card_id, reward_type) DO UPDATE
		SET twitch_reward_id = excluded.twitch_reward_id
	`, userID, CardID, twitchRewardID, rewardType)
	if err != nil {
		return fmt.Errorf("upsertTwitchReward: %w", err)
	}

	return nil
}
