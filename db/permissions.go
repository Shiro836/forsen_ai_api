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

func (db *DB) HasPermission(ctx context.Context, twitchUserID int, permission Permission) (bool, error) {
	var tmp int

	err := db.QueryRow(ctx, `
		SELECT
			1
		FROM permissions
		WHERE
			twitch_user_id = $1
		AND
			permission = $2
		AND
			status = $3
	`, twitchUserID, permission, PermissionStatusGranted).Scan(&tmp)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}

		return false, fmt.Errorf("failed to check permission: %w", err)
	}

	return true, nil
}

func (db *DB) AddPermission(ctx context.Context, initiator *User, targetTwitchUserID int, targetTwitchLogin string, permission Permission) error {
	if !IsValidPermission(permission) {
		return fmt.Errorf("invalid permission: %d", permission)
	}

	if hasPerm, err := db.HasPermission(ctx, targetTwitchUserID, permission); err != nil {
		return err
	} else if hasPerm {
		return nil
	}

	switch permission {
	case PermissionAdmin, PermissionMod:
		if hasAdmin, err := db.HasPermission(ctx, initiator.TwitchUserID, PermissionAdmin); err != nil {
			return err
		} else if !hasAdmin {
			return fmt.Errorf("%s doesn't have required permission: %s", initiator.TwitchLogin, PermissionAdmin.String())
		}
	case PermissionStreamer:
		if hasMod, err := db.HasPermission(ctx, initiator.TwitchUserID, PermissionMod); err != nil {
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

	if hasPerm, err := db.HasPermission(ctx, targetTwitchUserID, permission); err != nil {
		return err
	} else if !hasPerm {
		return nil
	}

	switch permission {
	case PermissionMod:
		if hasAdmin, err := db.HasPermission(ctx, initiator.TwitchUserID, PermissionAdmin); err != nil || !hasAdmin {
			return fmt.Errorf("%s doesn't have required permission: %s", initiator.TwitchLogin, PermissionAdmin.String())
		}
	case PermissionStreamer:
		if hasMod, err := db.HasPermission(ctx, initiator.TwitchUserID, PermissionMod); err != nil || !hasMod {
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
