package db

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

// One pair per user: the app only ever acts as the streamer server-side, and
// Twitch keeps older pairs valid, so the newest authorization is enough.
type TwitchTokens struct {
	AccessToken  string
	RefreshToken string
	Scopes       []string
}

func (db *DB) SetTwitchTokens(ctx context.Context, userID uuid.UUID, tokens *TwitchTokens) error {
	_, err := db.Exec(ctx, `
		INSERT INTO twitch_tokens (user_id, access_token, refresh_token, scopes, updated_at)
		VALUES ($1, $2, $3, $4, now())
		ON CONFLICT (user_id) DO UPDATE SET
			access_token = excluded.access_token,
			refresh_token = excluded.refresh_token,
			scopes = excluded.scopes,
			updated_at = now()
	`, userID, tokens.AccessToken, tokens.RefreshToken, tokens.Scopes)
	if err != nil {
		return fmt.Errorf("set twitch tokens: %w", err)
	}

	return nil
}

// A refresh never changes scopes.
func (db *DB) RefreshTwitchTokens(ctx context.Context, userID uuid.UUID, accessToken, refreshToken string) error {
	_, err := db.Exec(ctx, `
		UPDATE twitch_tokens
		SET access_token = $2, refresh_token = $3, updated_at = now()
		WHERE user_id = $1
	`, userID, accessToken, refreshToken)
	if err != nil {
		return fmt.Errorf("refresh twitch tokens: %w", err)
	}

	return nil
}

func (db *DB) GetTwitchTokens(ctx context.Context, userID uuid.UUID) (*TwitchTokens, error) {
	var tokens TwitchTokens

	err := db.QueryRow(ctx, `
		SELECT access_token, refresh_token, scopes
		FROM twitch_tokens
		WHERE user_id = $1
	`, userID).Scan(&tokens.AccessToken, &tokens.RefreshToken, &tokens.Scopes)
	if err != nil {
		return nil, fmt.Errorf("get twitch tokens: %w", parseErr(err))
	}

	return &tokens, nil
}
