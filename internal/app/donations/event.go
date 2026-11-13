// Package donations keeps a live connection per (streamer, provider) and
// turns provider events into queue rows. It knows nothing about Twitch.
package donations

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

// Event is one provider donation, already normalized. Amount is in the
// currency's minor units so no provider's float reaches the queue.
type Event struct {
	Provider    string
	ProviderID  string
	From        string
	Message     string
	AmountMinor int64
	Currency    string
	CreatedAt   time.Time
	Mock        bool
}

// Credentials is a saved token. ExpiresAt is a hint; adapters still handle a
// provider rejecting the token.
type Credentials struct {
	UserID       uuid.UUID
	AccessToken  string
	RefreshToken string
	ExpiresAt    *time.Time
	// LastEventAt is the backfill watermark; nil means the credential has
	// never delivered an event, so there is nothing to restore.
	LastEventAt *time.Time
}

// Session is what a source reports back to the supervisor.
type Session struct {
	Emit      func(Event)
	Connected func()
}

// Source serves one provider account until it fails. The supervisor owns
// reconnection and backoff.
type Source interface {
	Provider() string
	Run(ctx context.Context, creds Credentials, session Session) error
}

var (
	// ErrAuth means the provider rejected the credential; retrying cannot help.
	ErrAuth = errors.New("donation provider rejected the credential")
	// ErrExpired means the credential has expired and the streamer must reconnect.
	ErrExpired = errors.New("donation provider credential expired")
)
