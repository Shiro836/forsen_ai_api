package db

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/google/uuid"
)

const SessionTTL = 365 * 24 * time.Hour

func (db *DB) CreateSession(ctx context.Context, userID uuid.UUID) (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate session id: %w", err)
	}
	id := hex.EncodeToString(raw[:])

	_, err := db.Exec(ctx, `
		INSERT INTO user_sessions (id, user_id, expires_at)
		VALUES ($1, $2, $3)
	`, id, userID, time.Now().Add(SessionTTL))
	if err != nil {
		return "", fmt.Errorf("create session: %w", err)
	}

	if _, err := db.Exec(ctx, `DELETE FROM user_sessions WHERE expires_at < now()`); err != nil {
		return "", fmt.Errorf("purge expired sessions: %w", err)
	}

	return id, nil
}

func (db *DB) DeleteSession(ctx context.Context, sessionID string) error {
	if _, err := db.Exec(ctx, `DELETE FROM user_sessions WHERE id = $1`, sessionID); err != nil {
		return fmt.Errorf("delete session: %w", err)
	}

	return nil
}

func (db *DB) GetUserBySession(ctx context.Context, sessionID string) (*User, error) {
	var user User

	err := db.QueryRow(ctx, `
		SELECT
			u.id,
			u.twitch_login,
			u.twitch_user_id
		FROM user_sessions s
		JOIN users u ON u.id = s.user_id
		WHERE s.id = $1 AND s.expires_at > now()
	`, sessionID).Scan(&user.ID, &user.TwitchLogin, &user.TwitchUserID)
	if err != nil {
		return nil, fmt.Errorf("failed to get user by session: %w", parseErr(err))
	}

	return &user, nil
}
