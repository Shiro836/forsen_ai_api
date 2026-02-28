package db

import (
	"context"
	"fmt"
)

func (db *DB) VoiceShortNameExists(ctx context.Context, name string) (bool, error) {
	var exists bool
	err := db.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM char_cards
			WHERE lower(short_char_name) = lower($1)
			AND public = true
		)
	`, name).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("failed to check voice short name: %w", err)
	}
	return exists, nil
}

func (db *DB) SetChatUserVoice(ctx context.Context, twitchUserID int, twitchLogin, voice string) error {
	_, err := db.Exec(ctx, `
		INSERT INTO chat_users (twitch_user_id, twitch_login, voice)
		VALUES ($1, $2, $3)
		ON CONFLICT (twitch_user_id) DO UPDATE
		SET voice = $3, twitch_login = $2, updated_at = now()
	`, twitchUserID, twitchLogin, voice)
	if err != nil {
		return fmt.Errorf("failed to set chat user voice: %w", err)
	}
	return nil
}

func (db *DB) GetChatUserVoice(ctx context.Context, twitchUserID int) (string, error) {
	var voice *string
	err := db.QueryRow(ctx, `
		SELECT voice FROM chat_users WHERE twitch_user_id = $1
	`, twitchUserID).Scan(&voice)
	if err != nil {
		if ErrCode(err) == ErrCodeNoRows {
			return "", nil
		}
		return "", fmt.Errorf("failed to get chat user voice: %w", err)
	}
	if voice == nil {
		return "", nil
	}
	return *voice, nil
}

func (db *DB) IncrementChatUserRewardCount(ctx context.Context, twitchUserID int, twitchLogin string) error {
	_, err := db.Exec(ctx, `
		INSERT INTO chat_users (twitch_user_id, twitch_login, reward_count)
		VALUES ($1, $2, 1)
		ON CONFLICT (twitch_user_id) DO UPDATE
		SET reward_count = chat_users.reward_count + 1, twitch_login = $2, updated_at = now()
	`, twitchUserID, twitchLogin)
	if err != nil {
		return fmt.Errorf("failed to increment chat user reward count: %w", err)
	}
	return nil
}
