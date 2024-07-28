package api

import (
	"app/db"
	"context"
	"errors"
	"fmt"
)

// The purpose is to start processor workers for newly created users who already had permission, or already existing users who recieved necessary permission
// TODO: make the same for revoking permissions (? or deleting users ?)

func (api *API) handleNewUser(ctx context.Context, user *db.User) error {
	api.workersLock.Lock()
	defer api.workersLock.Unlock()

	if has, err := api.db.HasPermission(ctx, user.TwitchUserID, db.PermissionStreamer); err != nil {
		return fmt.Errorf("failed to check if user has permission: %w", err)
	} else if !has {
		return nil
	}

	api.connManager.HandleUser(user)

	return nil
}

func (api *API) handleNewTwitchUserID(ctx context.Context, twitchUserID int) error {
	api.workersLock.Lock()
	defer api.workersLock.Unlock()

	user, err := api.db.GetUserByTwitchUserID(ctx, twitchUserID)
	if errors.Is(err, db.ErrNoRows) {
		return nil
	}

	if err != nil {
		return fmt.Errorf("failed to get user by twitch user id: %w", err)
	}

	api.connManager.HandleUser(user)

	return nil
}
