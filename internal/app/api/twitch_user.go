package api

import (
	"app/db"
	"context"
	"fmt"

	"github.com/nicklaw5/helix/v2"
)

// helixForUser returns a Twitch client acting as user. Twitch access tokens
// live about four hours, so helix refreshes mid-request often; the new pair is
// written back so the next request doesn't start from an expired token again.
func (api *API) helixForUser(ctx context.Context, user *db.User) (*helix.Client, error) {
	tokens, err := api.db.GetTwitchTokens(ctx, user.ID)
	if err != nil {
		return nil, fmt.Errorf("twitch tokens for %s: %w", user.TwitchLogin, err)
	}

	client, err := api.twitchClient.NewHelixClient(tokens.AccessToken, tokens.RefreshToken)
	if err != nil {
		return nil, err
	}

	// helix fires the callback from its own goroutine, possibly after the
	// request that triggered it has finished.
	ctx = context.WithoutCancel(ctx)
	client.OnUserAccessTokenRefreshed(func(access, refresh string) {
		api.logger.Info("twitch token refreshed", "user", user.TwitchLogin)
		if err := api.db.RefreshTwitchTokens(ctx, user.ID, access, refresh); err != nil {
			api.logger.Error("failed to store refreshed twitch tokens", "user", user.TwitchLogin, "err", err)
		}
	})

	return client, nil
}
