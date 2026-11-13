package db

import (
	"context"
	"fmt"
	"strings"

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
}

func upsertDonationProviderSQL(userID uuid.UUID, provider, accessToken string, refreshToken *string, scopes []string) (string, []any, error) {
	return psql.Insert("donation_providers").
		Columns("user_id", "provider", "access_token", "refresh_token", "scopes", "updated_at").
		Values(userID, provider, accessToken, refreshToken, scopes, sq.Expr("now()")).
		Suffix("on conflict (user_id, provider) do update set access_token = excluded.access_token, refresh_token = excluded.refresh_token, scopes = excluded.scopes, updated_at = now()").
		ToSql()
}

func validDonationProvider(provider string) bool {
	switch provider {
	case ProviderStreamElements, ProviderStreamlabs, ProviderDonationAlerts:
		return true
	}
	return false
}

func (db *DB) UpsertDonationProvider(ctx context.Context, userID uuid.UUID, provider, accessToken string, refreshToken *string, scopes []string) error {
	if !validDonationProvider(provider) {
		return fmt.Errorf("upsert donation provider: unknown provider")
	}
	if strings.TrimSpace(accessToken) == "" {
		return fmt.Errorf("upsert donation provider: empty access token")
	}
	if scopes == nil {
		scopes = []string{}
	}

	query, args, err := upsertDonationProviderSQL(userID, provider, accessToken, refreshToken, scopes)
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
	return psql.Select("provider").
		From("donation_providers").
		Where(sq.Eq{"user_id": userID}).
		ToSql()
}

func (db *DB) ConnectedDonationProviders(ctx context.Context, userID uuid.UUID) (DonationProviderLinks, error) {
	var links DonationProviderLinks

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
		var provider string
		if err := rows.Scan(&provider); err != nil {
			return links, fmt.Errorf("connected donation providers: %w", err)
		}
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
