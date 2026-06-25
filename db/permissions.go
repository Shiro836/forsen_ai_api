package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type PermissionStatus int

const (
	PermissionStatusWaiting PermissionStatus = iota
	PermissionStatusGranted
	PermissionStatusDenied

	permissionStatusLast
)

func (s PermissionStatus) String() string {
	switch s {
	case PermissionStatusWaiting:
		return "Waiting"
	case PermissionStatusGranted:
		return "Granted"
	case PermissionStatusDenied:
		return "Denied"
	default:
		return "invalid"
	}
}

func IsValidStatus(status PermissionStatus) bool {
	return status < permissionStatusLast
}

type Permission int

const (
	PermissionStreamer Permission = iota
	PermissionMod
	PermissionAdmin

	permissionLast
)

func (p Permission) String() string {
	switch p {
	case PermissionStreamer:
		return "Streamer"
	case PermissionMod:
		return "Mod"
	case PermissionAdmin:
		return "Admin"
	default:
		return "invalid"
	}
}

func IsValidPermission(permission Permission) bool {
	return permission < permissionLast
}

func (db *DB) GetUsersPermissions(ctx context.Context, permission Permission, permissionStatus PermissionStatus) ([]*User, error) {
	if !IsValidPermission(permission) {
		return nil, fmt.Errorf("invalid permission: %d", permission)
	}

	rows, err := db.Query(ctx, `
		SELECT
			u.id,
			u.twitch_login,
			u.twitch_user_id,
			u.twitch_refresh_token,
			u.twitch_access_token
		FROM permissions as p
		join users as u ON p.twitch_user_id = u.twitch_user_id
		WHERE
			p.status = $1
		AND
			p.permission = $2
		ORDER BY p.id ASC
	`, permissionStatus, permission)
	if err != nil {
		return nil, fmt.Errorf("failed to get permitted users: %w", err)
	}
	defer rows.Close()

	var users []*User
	for rows.Next() {
		var user User
		err := rows.Scan(&user.ID, &user.TwitchLogin, &user.TwitchUserID, &user.TwitchRefreshToken, &user.TwitchAccessToken)
		if err != nil {
			return nil, fmt.Errorf("failed to scan permitted users: %w", err)
		}
		users = append(users, &user)
	}

	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to scan permitted users: %w", err)
	}

	return users, nil
}

type IngestUser struct {
	ID                uuid.UUID
	TwitchLogin       string
	TwitchUserID      int
	IngestAllMessages bool
}

func (db *DB) GetIngestUsers(ctx context.Context) ([]*IngestUser, error) {
	rows, err := db.Query(ctx, `
		SELECT
			u.id,
			u.twitch_login,
			u.twitch_user_id,
			coalesce((u.data->>'ingest_all_messages')::boolean, false)
		FROM permissions AS p
		JOIN users AS u ON p.twitch_user_id = u.twitch_user_id
		WHERE
			p.status = $1
		AND
			p.permission = $2
	`, PermissionStatusGranted, PermissionStreamer)
	if err != nil {
		return nil, fmt.Errorf("failed to get ingest users: %w", err)
	}
	defer rows.Close()

	var users []*IngestUser
	for rows.Next() {
		var u IngestUser
		if err := rows.Scan(&u.ID, &u.TwitchLogin, &u.TwitchUserID, &u.IngestAllMessages); err != nil {
			return nil, fmt.Errorf("failed to scan ingest user: %w", err)
		}
		users = append(users, &u)
	}

	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to scan ingest users: %w", err)
	}

	return users, nil
}

func (db *DB) GetUserPermissions(ctx context.Context, userID uuid.UUID, permissionStatus PermissionStatus) ([]Permission, error) {
	rows, err := db.Query(ctx, `
		SELECT
			p.permission
		FROM permissions p
		JOIN users u ON p.twitch_user_id = u.twitch_user_id
		WHERE
			u.id = $1
		and
			p.status = $2
	`, userID, permissionStatus)
	if err != nil {
		return nil, fmt.Errorf("failed to get user permissions: %w", err)
	}
	defer rows.Close()

	var permissions []Permission
	for rows.Next() {
		var permission Permission
		err := rows.Scan(&permission)
		if err != nil {
			return nil, fmt.Errorf("failed to scan user permissions: %w", err)
		}

		permissions = append(permissions, permission)
	}

	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to scan user permissions: %w", err)
	}

	return permissions, nil
}

func (db *DB) RequestAccess(ctx context.Context, user *User, permission Permission) error {
	if !IsValidPermission(permission) {
		return fmt.Errorf("invalid permission: %d", permission)
	}

	tag, err := db.Exec(ctx, `
		INSERT INTO permissions (
			twitch_login,
			twitch_user_id,
			status,
			permission
		)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (twitch_user_id, permission) DO NOTHING
	`, user.TwitchLogin, user.TwitchUserID, PermissionStatusWaiting, permission)
	if err != nil {
		return fmt.Errorf("failed to insert permission: %w", err)
	}

	if tag.RowsAffected() == 0 {
		return ErrAlreadyExists
	}

	return nil
}

// AutoGrantAccess grants the permission as granted only if the user has no
// existing permission record, so it never overrides a moderator's prior
// decision (e.g. a denied user). Returns true if a new grant was created.
// It is used for auto-approving newly created users.
func (db *DB) AutoGrantAccess(ctx context.Context, user *User, permission Permission) (bool, error) {
	if !IsValidPermission(permission) {
		return false, fmt.Errorf("invalid permission: %d", permission)
	}

	tag, err := db.Exec(ctx, `
		INSERT INTO permissions (twitch_login, twitch_user_id, permission, status)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (twitch_user_id, permission) DO NOTHING
	`, user.TwitchLogin, user.TwitchUserID, permission, PermissionStatusGranted)
	if err != nil {
		return false, fmt.Errorf("failed to auto grant access: %w", err)
	}

	return tag.RowsAffected() > 0, nil
}

func (db *DB) HasPermission(ctx context.Context, twitchUserID int, permission Permission) (bool, PermissionStatus, error) {
	var status PermissionStatus

	err := db.QueryRow(ctx, `
		SELECT
			status
		FROM permissions
		WHERE
			twitch_user_id = $1
		AND
			permission = $2
	`, twitchUserID, permission).Scan(&status)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, status, nil
		}

		return false, status, fmt.Errorf("failed to check permission: %w", err)
	}

	if status == PermissionStatusGranted {
		return true, status, nil
	}

	return false, status, nil
}

func (db *DB) AddPermission(ctx context.Context, initiator *User, targetTwitchUserID int, targetTwitchLogin string, permission Permission) error {
	if !IsValidPermission(permission) {
		return fmt.Errorf("invalid permission: %d", permission)
	}

	if hasPerm, _, err := db.HasPermission(ctx, targetTwitchUserID, permission); err != nil {
		return err
	} else if hasPerm {
		return nil
	}

	switch permission {
	case PermissionAdmin, PermissionMod:
		if hasAdmin, _, err := db.HasPermission(ctx, initiator.TwitchUserID, PermissionAdmin); err != nil {
			return err
		} else if !hasAdmin {
			return fmt.Errorf("%s doesn't have required permission: %s", initiator.TwitchLogin, PermissionAdmin.String())
		}
	case PermissionStreamer:
		if hasMod, _, err := db.HasPermission(ctx, initiator.TwitchUserID, PermissionMod); err != nil {
			return err
		} else if !hasMod {
			return fmt.Errorf("%s doesn't have required permission: %s", initiator.TwitchLogin, PermissionMod.String())
		}
	default:
		return fmt.Errorf("unknown permission: %d", permission)
	}

	_, err := db.Exec(ctx, `
		INSERT INTO permissions (twitch_login, twitch_user_id, permission, status)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (twitch_user_id, permission)
		DO UPDATE SET
			status = excluded.status
			, updated_at = NOW()
	`, targetTwitchLogin, targetTwitchUserID, permission, PermissionStatusGranted)
	if err != nil {
		return fmt.Errorf("failed to insert permission: %w", err)
	}

	return nil
}

func (db *DB) RemovePermission(ctx context.Context, initiator *User, targetTwitchUserID int, permission Permission) error {
	if !IsValidPermission(permission) {
		return fmt.Errorf("invalid permission: %d", permission)
	}

	if hasPerm, status, err := db.HasPermission(ctx, targetTwitchUserID, permission); err != nil {
		return err
	} else if !hasPerm && status == PermissionStatusDenied {
		return nil
	}

	switch permission {
	case PermissionMod:
		if hasAdmin, _, err := db.HasPermission(ctx, initiator.TwitchUserID, PermissionAdmin); err != nil || !hasAdmin {
			return fmt.Errorf("%s doesn't have required permission: %s", initiator.TwitchLogin, PermissionAdmin.String())
		}
	case PermissionStreamer:
		if hasMod, _, err := db.HasPermission(ctx, initiator.TwitchUserID, PermissionMod); err != nil || !hasMod {
			return fmt.Errorf("%s doesn't have required permission: %s", initiator.TwitchLogin, PermissionMod.String())
		}
	default:
		return fmt.Errorf("unknown permission: %d", permission)
	}

	_, err := db.Exec(ctx, `
		UPDATE
			permissions
		SET
			status = $1
			, updated_at = NOW()
		WHERE
			twitch_user_id = $2
		AND
			permission = $3
	`, PermissionStatusDenied, targetTwitchUserID, permission)
	if err != nil {
		return fmt.Errorf("failed to delete permission: %w", err)
	}

	return nil
}

func (db *DB) GetUsersWithDeniedPermission(ctx context.Context, permission Permission) ([]*User, error) {
	if !IsValidPermission(permission) {
		return nil, fmt.Errorf("invalid permission: %d", permission)
	}

	rows, err := db.Query(ctx, `
		SELECT
			u.id,
			u.twitch_login,
			u.twitch_user_id,
			u.twitch_refresh_token,
			u.twitch_access_token
		FROM users u
		JOIN permissions p ON p.twitch_user_id = u.twitch_user_id
		WHERE p.permission = $1
		AND p.status = $2
		ORDER BY u.twitch_login
	`, permission, PermissionStatusDenied)
	if err != nil {
		return nil, fmt.Errorf("failed to get users with denied permission: %w", err)
	}
	defer rows.Close()

	var users []*User
	for rows.Next() {
		var user User
		err := rows.Scan(&user.ID, &user.TwitchLogin, &user.TwitchUserID, &user.TwitchRefreshToken, &user.TwitchAccessToken)
		if err != nil {
			return nil, fmt.Errorf("failed to scan users with denied permission: %w", err)
		}
		users = append(users, &user)
	}

	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to scan users with denied permission: %w", err)
	}

	return users, nil
}
