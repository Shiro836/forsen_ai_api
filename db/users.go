package db

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

const (
	DefaultTtsLimitSeconds = 80
	DefaultMaxSfxCount     = 10
)

type User struct {
	ID uuid.UUID

	TwitchLogin  string
	TwitchUserID int

	TwitchRefreshToken string
	TwitchAccessToken  string

	Session string
}

func (db *DB) UpsertUser(ctx context.Context, user *User) (uuid.UUID, error) {
	var id uuid.UUID

	err := db.QueryRow(ctx, `
		INSERT INTO users (
			twitch_login,
			twitch_user_id,
			twitch_refresh_token,
			twitch_access_token,
			session
		) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (twitch_user_id) DO UPDATE SET
			twitch_login = excluded.twitch_login,
			twitch_refresh_token = excluded.twitch_refresh_token,
			twitch_access_token = excluded.twitch_access_token,
			session = excluded.session
		RETURNING id
	`,
		user.TwitchLogin,
		user.TwitchUserID,
		user.TwitchRefreshToken,
		user.TwitchAccessToken,
		user.Session,
	).Scan(&id)

	if err != nil {
		return uuid.Nil, fmt.Errorf("upsert user: %w", err)
	}

	return id, nil
}

func (db *DB) GetUserByID(ctx context.Context, userID uuid.UUID) (*User, error) {
	var user User

	err := db.QueryRow(ctx, `
		SELECT
			id,
			twitch_login,
			twitch_user_id,
			twitch_refresh_token,
			twitch_access_token,
			session
		FROM users
		WHERE id = $1
	`, userID).Scan(
		&user.ID,
		&user.TwitchLogin,
		&user.TwitchUserID,
		&user.TwitchRefreshToken,
		&user.TwitchAccessToken,
		&user.Session,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to get user by id: %w", parseErr(err))
	}

	return &user, nil
}

func (db *DB) GetUserByTwitchLogin(ctx context.Context, twitchLogin string) (*User, error) {
	var user User

	err := db.QueryRow(ctx, `
		SELECT
			id,
			twitch_login,
			twitch_user_id,
			twitch_refresh_token,
			twitch_access_token,
			session
		FROM users
		WHERE lower(twitch_login) = lower($1)
	`, twitchLogin).Scan(
		&user.ID,
		&user.TwitchLogin,
		&user.TwitchUserID,
		&user.TwitchRefreshToken,
		&user.TwitchAccessToken,
		&user.Session,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to get user by twitch login: %w", parseErr(err))
	}

	return &user, nil
}

func (db *DB) GetUserByTwitchUserID(ctx context.Context, twitchUserID int) (*User, error) {
	var user User

	err := db.QueryRow(ctx, `
		SELECT
			id,
			twitch_login,
			twitch_user_id,
			twitch_refresh_token,
			twitch_access_token,
			session
		FROM users
		WHERE twitch_user_id = $1
	`, twitchUserID).Scan(
		&user.ID,
		&user.TwitchLogin,
		&user.TwitchUserID,
		&user.TwitchRefreshToken,
		&user.TwitchAccessToken,
		&user.Session,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to get user by twitch user id: %w", parseErr(err))
	}

	return &user, nil
}

func (db *DB) GetUserBySession(ctx context.Context, session string) (*User, error) {
	var user User

	err := db.QueryRow(ctx, `
		SELECT
			id,
			twitch_login,
			twitch_user_id,
			twitch_refresh_token,
			twitch_access_token,
			session
		FROM users
		WHERE session = $1
	`, session).Scan(
		&user.ID,
		&user.TwitchLogin,
		&user.TwitchUserID,
		&user.TwitchRefreshToken,
		&user.TwitchAccessToken,
		&user.Session,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to get user by session: %w", parseErr(err))
	}

	return &user, nil
}

type UserSettings struct {
	Filters        string        `json:"filters"`
	RequestTimeout time.Duration `json:"requestTimeout"`
	TtsLimit       *int          `json:"tts_limit,omitempty"`     // Maximum TTS audio length in seconds (nil = not set, 0 = use default 80s)
	MaxSfxCount    *int          `json:"max_sfx_count,omitempty"` // Maximum number of SFX that can be used in a single TTS message (nil = not set, 0 = unlimited)
}

func (db *DB) UpdateUserData(ctx context.Context, userID uuid.UUID, settings *UserSettings) error {
	_, err := db.Exec(ctx, `
		UPDATE users
		SET
			data = $1
		WHERE id = $2
	`, settings, userID)
	if err != nil {
		return fmt.Errorf("failed to update user data: %w", err)
	}

	return nil
}

func (db *DB) GetUserSettings(ctx context.Context, userID uuid.UUID) (*UserSettings, error) {
	var settings UserSettings

	err := db.QueryRow(ctx, `
		SELECT
			data
		FROM users
		WHERE id = $1
	`, userID).Scan(&settings)
	if err != nil {
		return nil, fmt.Errorf("failed to get user settings: %w", err)
	}

	// Set default values for new fields that might not exist in old user data
	if settings.TtsLimit == nil {
		defaultTtsLimit := DefaultTtsLimitSeconds
		settings.TtsLimit = &defaultTtsLimit
	}

	if settings.MaxSfxCount == nil {
		defaultMaxSfxCount := DefaultMaxSfxCount
		settings.MaxSfxCount = &defaultMaxSfxCount // Default to 10 SFX per message
	}

	return &settings, nil
}
