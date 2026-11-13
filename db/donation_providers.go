package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	sq "github.com/Masterminds/squirrel"
	"github.com/google/uuid"
)

const (
	ProviderStreamElements = "streamelements"
	ProviderStreamlabs     = "streamlabs"
	ProviderDonationAlerts = "donationalerts"
)

// DonationProviderLinks reports which providers have a saved row.
type DonationProviderLinks struct {
	StreamElements bool
	Streamlabs     bool
	DonationAlerts bool

	// State is keyed by provider; a provider with no saved row has no entry.
	State map[string]DonationProviderState
}

// DonationProviderState is what the streamer's page shows about a saved row.
type DonationProviderState struct {
	Connected bool
	Status    string
	Error     string
	ExpiresAt *time.Time
}

// DonationProviderToken is one saved credential, as the ingest service reads it.
type DonationProviderToken struct {
	UserID       uuid.UUID
	Provider     string
	AccessToken  string
	RefreshToken string
	ExpiresAt    *time.Time
	LastEventAt  *time.Time
	UpdatedAt    time.Time
}

func upsertDonationProviderSQL(userID uuid.UUID, provider, accessToken string, refreshToken *string, scopes []string, expiresAt *time.Time) (string, []any, error) {
	return psql.Insert("donation_providers").
		Columns("user_id", "provider", "access_token", "refresh_token", "scopes", "expires_at", "updated_at").
		Values(userID, provider, accessToken, refreshToken, scopes, expiresAt, sq.Expr("now()")).
		Suffix("on conflict (user_id, provider) do update set access_token = excluded.access_token, refresh_token = excluded.refresh_token, scopes = excluded.scopes, expires_at = excluded.expires_at, updated_at = now()").
		ToSql()
}

func validDonationProvider(provider string) bool {
	switch provider {
	case ProviderStreamElements, ProviderStreamlabs, ProviderDonationAlerts:
		return true
	}
	return false
}

func (db *DB) UpsertDonationProvider(ctx context.Context, userID uuid.UUID, provider, accessToken string, refreshToken *string, scopes []string, expiresAt *time.Time) error {
	if !validDonationProvider(provider) {
		return fmt.Errorf("upsert donation provider: unknown provider")
	}
	if strings.TrimSpace(accessToken) == "" {
		return fmt.Errorf("upsert donation provider: empty access token")
	}
	if scopes == nil {
		scopes = []string{}
	}

	query, args, err := upsertDonationProviderSQL(userID, provider, accessToken, refreshToken, scopes, expiresAt)
	if err != nil {
		return fmt.Errorf("upsert donation provider: %w", err)
	}
	if _, err := db.Exec(ctx, query, args...); err != nil {
		return fmt.Errorf("upsert donation provider: %w", err)
	}
	return nil
}

func deleteDonationProviderSQL(userID uuid.UUID, provider string) (string, []any, error) {
	return psql.Delete("donation_providers").
		Where(sq.Eq{"user_id": userID, "provider": provider}).
		ToSql()
}

func (db *DB) DeleteDonationProvider(ctx context.Context, userID uuid.UUID, provider string) error {
	if !validDonationProvider(provider) {
		return fmt.Errorf("delete donation provider: unknown provider")
	}

	query, args, err := deleteDonationProviderSQL(userID, provider)
	if err != nil {
		return fmt.Errorf("delete donation provider: %w", err)
	}
	if _, err := db.Exec(ctx, query, args...); err != nil {
		return fmt.Errorf("delete donation provider: %w", err)
	}
	return nil
}

func connectedDonationProvidersSQL(userID uuid.UUID) (string, []any, error) {
	return psql.Select("provider", "status", "status_error", "expires_at").
		From("donation_providers").
		Where(sq.Eq{"user_id": userID}).
		ToSql()
}

func (db *DB) ConnectedDonationProviders(ctx context.Context, userID uuid.UUID) (DonationProviderLinks, error) {
	links := DonationProviderLinks{State: make(map[string]DonationProviderState)}

	query, args, err := connectedDonationProvidersSQL(userID)
	if err != nil {
		return links, fmt.Errorf("connected donation providers: %w", err)
	}

	rows, err := db.Query(ctx, query, args...)
	if err != nil {
		return links, fmt.Errorf("connected donation providers: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			state     DonationProviderState
			provider  string
			expiresAt sql.NullTime
		)
		if err := rows.Scan(&provider, &state.Status, &state.Error, &expiresAt); err != nil {
			return links, fmt.Errorf("connected donation providers: %w", err)
		}
		if expiresAt.Valid {
			state.ExpiresAt = &expiresAt.Time
		}
		state.Connected = true
		links.State[provider] = state

		switch provider {
		case ProviderStreamElements:
			links.StreamElements = true
		case ProviderStreamlabs:
			links.Streamlabs = true
		case ProviderDonationAlerts:
			links.DonationAlerts = true
		}
	}
	if err := rows.Err(); err != nil {
		return links, fmt.Errorf("connected donation providers: %w", err)
	}
	return links, nil
}

func listDonationProviderTokensSQL() (string, []any, error) {
	return psql.Select("user_id", "provider", "access_token", "coalesce(refresh_token, '')", "expires_at", "last_event_at", "updated_at").
		From("donation_providers").
		ToSql()
}

// ListDonationProviderTokens returns every saved credential. The ingest service
// intersects this with the users it may act for; it does not filter here.
func (db *DB) ListDonationProviderTokens(ctx context.Context) ([]DonationProviderToken, error) {
	query, args, err := listDonationProviderTokensSQL()
	if err != nil {
		return nil, fmt.Errorf("list donation provider tokens: %w", err)
	}

	rows, err := db.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list donation provider tokens: %w", err)
	}
	defer rows.Close()

	var tokens []DonationProviderToken
	for rows.Next() {
		var (
			token       DonationProviderToken
			expiresAt   sql.NullTime
			lastEventAt sql.NullTime
		)
		if err := rows.Scan(&token.UserID, &token.Provider, &token.AccessToken, &token.RefreshToken, &expiresAt, &lastEventAt, &token.UpdatedAt); err != nil {
			return nil, fmt.Errorf("list donation provider tokens: %w", err)
		}
		if expiresAt.Valid {
			token.ExpiresAt = &expiresAt.Time
		}
		if lastEventAt.Valid {
			token.LastEventAt = &lastEventAt.Time
		}
		tokens = append(tokens, token)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list donation provider tokens: %w", err)
	}
	return tokens, nil
}

func updateDonationProviderTokensSQL(userID uuid.UUID, provider, accessToken, refreshToken string, expiresAt *time.Time) (string, []any, error) {
	return psql.Update("donation_providers").
		Set("access_token", accessToken).
		Set("refresh_token", sq.Expr("nullif(?, '')", refreshToken)).
		Set("expires_at", expiresAt).
		// updated_at is the user's action marker; a refresh must not look like
		// the streamer reconnecting the provider.
		Where(sq.Eq{"user_id": userID, "provider": provider}).
		ToSql()
}

func (db *DB) UpdateDonationProviderTokens(ctx context.Context, userID uuid.UUID, provider, accessToken, refreshToken string, expiresAt *time.Time) error {
	if !validDonationProvider(provider) {
		return fmt.Errorf("update donation provider tokens: unknown provider")
	}
	if strings.TrimSpace(accessToken) == "" {
		return fmt.Errorf("update donation provider tokens: empty access token")
	}

	query, args, err := updateDonationProviderTokensSQL(userID, provider, accessToken, refreshToken, expiresAt)
	if err != nil {
		return fmt.Errorf("update donation provider tokens: %w", err)
	}
	if _, err := db.Exec(ctx, query, args...); err != nil {
		return fmt.Errorf("update donation provider tokens: %w", err)
	}
	return nil
}

func setDonationProviderStatusSQL(userID uuid.UUID, provider, status, statusError string) (string, []any, error) {
	return psql.Update("donation_providers").
		Set("status", status).
		Set("status_error", statusError).
		Set("status_updated_at", sq.Expr("now()")).
		Where(sq.Eq{"user_id": userID, "provider": provider}).
		ToSql()
}

// AdvanceDonationProviderLastEvent moves the backfill watermark forward. The
// greatest() keeps a late event from moving it backwards.
func (db *DB) AdvanceDonationProviderLastEvent(ctx context.Context, userID uuid.UUID, provider string, at time.Time) error {
	if !validDonationProvider(provider) {
		return fmt.Errorf("advance donation provider last event: unknown provider")
	}
	if at.IsZero() {
		return nil
	}

	query, args, err := psql.Update("donation_providers").
		Set("last_event_at", sq.Expr("greatest(coalesce(last_event_at, ?), ?)", at, at)).
		Where(sq.Eq{"user_id": userID, "provider": provider}).
		ToSql()
	if err != nil {
		return fmt.Errorf("advance donation provider last event: %w", err)
	}
	if _, err := db.Exec(ctx, query, args...); err != nil {
		return fmt.Errorf("advance donation provider last event: %w", err)
	}
	return nil
}

// SetDonationProviderStatus stores the connection outcome the streamer's page
// reads. A disconnected provider has no row, so the update is a no-op there.
func (db *DB) SetDonationProviderStatus(ctx context.Context, userID uuid.UUID, provider, status, statusError string) error {
	if !validDonationProvider(provider) {
		return fmt.Errorf("set donation provider status: unknown provider")
	}

	query, args, err := setDonationProviderStatusSQL(userID, provider, status, statusError)
	if err != nil {
		return fmt.Errorf("set donation provider status: %w", err)
	}
	if _, err := db.Exec(ctx, query, args...); err != nil {
		return fmt.Errorf("set donation provider status: %w", err)
	}
	return nil
}
