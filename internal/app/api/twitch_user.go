package api

import (
	"app/db"
	"context"

	"github.com/nicklaw5/helix/v2"
)

// helixForUser returns a Twitch client acting as user. Twitch access tokens
// live about four hours, so helix refreshes mid-request often; the new pair is
// written back so the next request doesn't start from an expired token again.
func (api *API) helixForUser(ctx context.Context, user *db.User) (*helix.Client, error) {
	client, err := api.twitchClient.NewHelixClient(user.TwitchAccessToken, user.TwitchRefreshToken)
	if err != nil {
		return nil, err
	}

	// helix fires the callback from its own goroutine, possibly after the
	// request that triggered it has finished.
	ctx = context.WithoutCancel(ctx)
	client.OnUserAccessTokenRefreshed(func(access, refresh string) {
		api.logger.Info("twitch token refreshed", "user", user.TwitchLogin)
		if err := api.db.UpdateUserTokens(ctx, user.ID, access, refresh); err != nil {
			api.logger.Error("failed to store refreshed twitch tokens", "user", user.TwitchLogin, "err", err)
		}
	})

	return client, nil
}
