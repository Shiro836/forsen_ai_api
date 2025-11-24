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
	TwitchRewardUniversalTTS
	TwitchRewardAgentic
)

func (t TwitchRewardType) String() string {
	switch t {
	case TwitchRewardTTS:
		return "TTS"
	case TwitchRewardAI:
		return "AI"
	case TwitchRewardUniversalTTS:
		return "BAJ TTS"
	case TwitchRewardAgentic:
		return "Agent BAJ"
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

func (db *DB) UpsertTwitchReward(ctx context.Context, userID uuid.UUID, CardID *uuid.UUID, twitchRewardID string, rewardType TwitchRewardType) error {
	var query string
	if CardID == nil {
		query = `
			INSERT INTO reward_buttons (
				user_id,
				card_id,
				twitch_reward_id,
				reward_type
			)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (user_id, reward_type) WHERE card_id IS NULL DO UPDATE
			SET twitch_reward_id = excluded.twitch_reward_id
		`
	} else {
		query = `
			INSERT INTO reward_buttons (
				user_id,
				card_id,
				twitch_reward_id,
				reward_type
			)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (user_id, card_id, reward_type) WHERE card_id IS NOT NULL DO UPDATE
			SET twitch_reward_id = excluded.twitch_reward_id
		`
	}

	_, err := db.Exec(ctx, query, userID, CardID, twitchRewardID, rewardType)
	if err != nil {
		return fmt.Errorf("upsertTwitchReward: %w", err)
	}

	return nil
}

func (db *DB) GetRewardByTwitchReward(ctx context.Context, userID uuid.UUID, twitchRewardID string) (*uuid.UUID, TwitchRewardType, error) {
	var cardID *uuid.UUID
	var rewardType TwitchRewardType

	err := db.QueryRow(ctx, `
		SELECT card_id, reward_type
		FROM reward_buttons
		WHERE twitch_reward_id = $1 AND user_id = $2
	`, twitchRewardID, userID).Scan(&cardID, &rewardType)
	if err != nil {
		return nil, 0, fmt.Errorf("getRewardByTwitchReward: %w", err)
	}

	return cardID, rewardType, nil
}

func (db *DB) UpsertUniversalTTSReward(ctx context.Context, userID uuid.UUID, twitchRewardID string) error {
	return db.UpsertTwitchReward(ctx, userID, nil, twitchRewardID, TwitchRewardUniversalTTS)
}

func (db *DB) UpsertAgenticReward(ctx context.Context, userID uuid.UUID, twitchRewardID string) error {
	return db.UpsertTwitchReward(ctx, userID, nil, twitchRewardID, TwitchRewardAgentic)
}
