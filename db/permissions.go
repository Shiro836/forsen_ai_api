package db

import "context"

const (
	PermissionStreamer = "streamer"
)

func (db *DB) GetPermittedUsers(ctx context.Context, permission string) ([]*User, error) {
	query := `
		SELECT
			u.id,
			u.twitch_login,
			u.twitch_user_id,
			u.twitch_refresh_token,
			u.twitch_access_token
		FROM permissions as p
		right join users as u ON p.twitch_user_id = u.twitch_user_id
		WHERE p.status = $1
	`

	rows, err := db.db.Query(ctx, query, permission)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []*User
	for rows.Next() {
		var user User
		err := rows.Scan(&user.ID, &user.TwitchLogin, &user.TwitchUserID, &user.TwitchRefreshToken, &user.TwitchAccessToken)
		if err != nil {
			return nil, err
		}
		users = append(users, &user)
	}

	if err = rows.Err(); err != nil {
		return nil, err
	}

	return users, nil
}
