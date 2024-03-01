package db

import (
	"context"
	"fmt"
)

type User struct {
	ID int

	TwitchLogin  string
	TwitchUserID int

	TwitchRefreshToken string
	TwitchAccessToken  string

	Session string
}

func (db *DB) GetUserByID(ctx context.Context, ID int) (*User, error) {
	var user User

	err := db.db.QueryRow(ctx, `
		SELECT
			id,
			twitch_login,
			twitch_user_id,
			twitch_refresh_token,
			twitch_access_token,
			session
		FROM users
		WHERE id = $1
	`, ID).Scan(
		&user.ID,
		&user.TwitchLogin,
		&user.TwitchUserID,
		&user.TwitchRefreshToken,
		&user.TwitchAccessToken,
		&user.Session,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to get user by id: %w", err)
	}

	return &user, nil
}

func (db *DB) GetUserByTwitchLogin(ctx context.Context, twitchLogin string) (*User, error) {
	var user User

	err := db.db.QueryRow(ctx, `
		SELECT
			id,
			twitch_login,
			twitch,
			twitch_refresh_token,
			twitch_access_token,
			session
		FROM users
		WHERE lower(twitch_login) = $1
	`, twitchLogin).Scan(
		&user.ID,
		&user.TwitchLogin,
		&user.TwitchUserID,
		&user.TwitchRefreshToken,
		&user.TwitchAccessToken,
		&user.Session,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to get user by twitch login: %w", err)
	}

	return &user, nil
}

func (db *DB) GetUserBySession(ctx context.Context, session string) (*User, error) {
	var user User

	err := db.db.QueryRow(ctx, `
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
		return nil, fmt.Errorf("failed to get user by session: %w", err)
	}

	return &user, nil
}

func (db *DB) GetGoScript(ctx context.Context, userID int) string {
	panic("not implemented")
}

func (db *DB) GetFilters(ctx context.Context, userID int) (string, error) {
	panic("not implemented")
}
