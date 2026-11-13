package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	sq "github.com/Masterminds/squirrel"
)

// DonationRates is the last good units-per-USD table. It outlives a restart so
// a failed refresh does not leave intake without rates.
type DonationRates struct {
	Rates     map[string]float64
	FetchedAt time.Time
}

func (db *DB) GetDonationRates(ctx context.Context) (*DonationRates, error) {
	var (
		raw       []byte
		fetchedAt time.Time
	)
	err := db.QueryRow(ctx, `select rates, fetched_at from donation_rates where id = 1`).Scan(&raw, &fetchedAt)
	if err != nil {
		if errors.Is(err, ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("get donation rates: %w", parseErr(err))
	}

	var rates map[string]float64
	if err := json.Unmarshal(raw, &rates); err != nil {
		return nil, fmt.Errorf("get donation rates: %w", err)
	}
	return &DonationRates{Rates: rates, FetchedAt: fetchedAt}, nil
}

func (db *DB) SetDonationRates(ctx context.Context, rates map[string]float64, fetchedAt time.Time) error {
	encoded, err := json.Marshal(rates)
	if err != nil {
		return fmt.Errorf("set donation rates: %w", err)
	}

	query, args, err := psql.Insert("donation_rates").
		Columns("id", "rates", "fetched_at").
		Values(1, sq.Expr("?::jsonb", string(encoded)), fetchedAt).
		Suffix("on conflict (id) do update set rates = excluded.rates, fetched_at = excluded.fetched_at").
		ToSql()
	if err != nil {
		return fmt.Errorf("set donation rates: %w", err)
	}
	if _, err := db.Exec(ctx, query, args...); err != nil {
		return fmt.Errorf("set donation rates: %w", err)
	}
	return nil
}
