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
	TwitchRewardUniversalAI
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
	case TwitchRewardUniversalAI:
		return "BAJ AI"
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

// UpsertTwitchReward binds a Twitch reward to a character, or with a nil card
// to a special reward type. A character can hold a points reward and a bits
// power-up at once, so bindings are keyed by reward id and never replace each
// other.
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
			ON CONFLICT (user_id, reward_type, twitch_reward_id) WHERE card_id IS NULL DO NOTHING
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
			ON CONFLICT (user_id, card_id, reward_type, twitch_reward_id) WHERE card_id IS NOT NULL DO NOTHING
		`
	}

	_, err := db.Exec(ctx, query, userID, CardID, twitchRewardID, rewardType)
	if err != nil {
		return fmt.Errorf("upsertTwitchReward: %w", err)
	}

	return nil
}

func (db *DB) GetRewardByTwitchReward(ctx context.Context, twitchRewardID string) (*uuid.UUID, TwitchRewardType, error) {
	var cardID *uuid.UUID
	var rewardType TwitchRewardType

	err := db.QueryRow(ctx, `
		SELECT card_id, reward_type
		FROM reward_buttons
		WHERE twitch_reward_id = $1
	`, twitchRewardID).Scan(&cardID, &rewardType)
	if err != nil {
		return nil, 0, fmt.Errorf("getRewardByTwitchReward: %w", parseErr(err))
	}

	return cardID, rewardType, nil
}

func (db *DB) UpsertUniversalTTSReward(ctx context.Context, userID uuid.UUID, twitchRewardID string) error {
	return db.UpsertTwitchReward(ctx, userID, nil, twitchRewardID, TwitchRewardUniversalTTS)
}

func (db *DB) UpsertAgenticReward(ctx context.Context, userID uuid.UUID, twitchRewardID string) error {
	return db.UpsertTwitchReward(ctx, userID, nil, twitchRewardID, TwitchRewardAgentic)
}

func (db *DB) UpsertUniversalAIReward(ctx context.Context, userID uuid.UUID, twitchRewardID string) error {
	return db.UpsertTwitchReward(ctx, userID, nil, twitchRewardID, TwitchRewardUniversalAI)
}

// IsRewardBound reports whether a twitch reward id is already linked to a
// character or a special reward.
func (db *DB) IsRewardBound(ctx context.Context, twitchRewardID string) (bool, error) {
	var bound bool

	err := db.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM reward_buttons
			WHERE twitch_reward_id = $1
		)
	`, twitchRewardID).Scan(&bound)
	if err != nil {
		return false, fmt.Errorf("isRewardBound: %w", err)
	}

	return bound, nil
}
