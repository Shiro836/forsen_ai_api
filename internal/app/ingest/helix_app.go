package ingest

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"app/pkg/twitch"

	"github.com/Its-donkey/kappopher/helix"
)

// appClient is helix under the app's own token, for calls no streamer has to
// authorize.
type appClient struct {
	*helix.Client
	auth *helix.AuthClient
}

func newAppClient(cfg *twitch.Config) *appClient {
	auth := helix.NewAuthClient(helix.AuthConfig{
		ClientID:     cfg.ClientID,
		ClientSecret: cfg.Secret,
	})
	return &appClient{Client: helix.NewClient(cfg.ClientID, auth), auth: auth}
}

// App tokens carry no refresh token; an expired one is simply requested again.
func (a *appClient) call(ctx context.Context, fn func() error) error {
	if a.auth.GetToken() == nil {
		if _, err := a.auth.GetAppAccessToken(ctx); err != nil {
			return fmt.Errorf("get app token: %w", err)
		}
	}

	err := fn()

	var apiErr *helix.APIError
	if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusUnauthorized {
		if _, err := a.auth.GetAppAccessToken(ctx); err != nil {
			return fmt.Errorf("renew app token: %w", err)
		}
		return fn()
	}
	return err
}
