package api

import (
	"app/db"
	"context"

	"github.com/nicklaw5/helix/v2"
)

func (api *API) helixForUser(ctx context.Context, user *db.User) (*helix.Client, error) {
	return api.twitchClient.HelixForUser(ctx, api.logger, api.db, user)
}
