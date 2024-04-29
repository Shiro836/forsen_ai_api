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

func (db *DB) UpsertUser(ctx context.Context, user *User) (int, error) {
	var id int

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
		return -1, fmt.Errorf("upsert user: %w", err)
	}

	return id, nil
}

func (db *DB) GetUserByID(ctx context.Context, userID int) (*User, error) {
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
		return nil, fmt.Errorf("failed to get user by id: %w", err)
	}

	return &user, nil
}

func (db *DB) GetUserByTwitchLogin(ctx context.Context, twitchLogin string) (*User, error) {
	var user User

	err := db.QueryRow(ctx, `
		SELECT
			id,
			twitch_login,
			twitch,
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
		return nil, fmt.Errorf("failed to get user by twitch login: %w", err)
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

func (db *DB) GetGoScript(ctx context.Context, userID int) string {
	panic("not implemented")
}

func (db *DB) GetFilters(ctx context.Context, userID int) (string, error) {
	panic("not implemented")
}
