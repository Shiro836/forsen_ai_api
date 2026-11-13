package donations

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"app/db"
)

const (
	seRatesURL  = "https://api.streamelements.com/kappa/v2/tipping/rates"
	erAPIURL    = "https://open.er-api.com/v6/latest/USD"
	ratesMaxAge = 24 * time.Hour
)

// Rates is the units-per-USD table used to give every paid row one comparable
// value. A stale table is better than none; a missing currency is not guessed.
type Rates struct {
	logger *slog.Logger
	db     *db.DB
	client *http.Client

	mu        sync.RWMutex
	table     map[string]float64
	fetchedAt time.Time
}

func NewRates(logger *slog.Logger, database *db.DB) *Rates {
	return &Rates{
		logger: logger,
		db:     database,
		client: &http.Client{Timeout: 15 * time.Second},
	}
}

// Run loads the last good table, then refreshes it daily.
func (r *Rates) Run(ctx context.Context) {
	if saved, err := r.db.GetDonationRates(ctx); err != nil {
		r.logger.Error("failed to load donation rates", "err", err)
	} else if saved != nil {
		r.set(saved.Rates, saved.FetchedAt)
		r.logger.Info("loaded donation rates", "currencies", len(saved.Rates), "fetched_at", saved.FetchedAt)
	}

	r.refresh(ctx)

	ticker := time.NewTicker(ratesMaxAge)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.refresh(ctx)
		}
	}
}

func (r *Rates) refresh(ctx context.Context) {
	table, err := r.fetchStreamElements(ctx)
	if err != nil {
		r.logger.Warn("streamelements rates failed, trying backup", "err", err)
		table, err = r.fetchERAPI(ctx)
	}
	if err != nil {
		r.logger.Error("failed to refresh donation rates, keeping the last table", "err", err)
		return
	}

	fetchedAt := time.Now().UTC()
	r.set(table, fetchedAt)
	if err := r.db.SetDonationRates(ctx, table, fetchedAt); err != nil {
		r.logger.Error("failed to persist donation rates", "err", err)
	}
	r.logger.Info("refreshed donation rates", "currencies", len(table))
}

func (r *Rates) fetchStreamElements(ctx context.Context) (map[string]float64, error) {
	var table map[string]float64
	if err := r.fetchJSON(ctx, seRatesURL, &table); err != nil {
		return nil, err
	}
	if err := validRates(table); err != nil {
		return nil, fmt.Errorf("streamelements rates: %w", err)
	}
	return table, nil
}

func (r *Rates) fetchERAPI(ctx context.Context) (map[string]float64, error) {
	var body struct {
		Result string             `json:"result"`
		Rates  map[string]float64 `json:"rates"`
	}
	if err := r.fetchJSON(ctx, erAPIURL, &body); err != nil {
		return nil, err
	}
	if err := validRates(body.Rates); err != nil {
		return nil, fmt.Errorf("er-api rates: %w", err)
	}
	return body.Rates, nil
}

func (r *Rates) fetchJSON(ctx context.Context, url string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := r.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(out)
}

func validRates(table map[string]float64) error {
	if len(table) < 10 {
		return fmt.Errorf("only %d currencies", len(table))
	}
	if table["USD"] != 1 {
		return fmt.Errorf("USD rate is %v, not 1", table["USD"])
	}
	return nil
}

func (r *Rates) set(table map[string]float64, fetchedAt time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.table = table
	r.fetchedAt = fetchedAt
}

func (r *Rates) Rate(currency string) (float64, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	rate, ok := r.table[normalizeCurrency(currency)]
	return rate, ok && rate > 0
}

// USD returns the value in minor USD units and the rate used. ok is false when
// the table has no rate for the currency.
func (r *Rates) USD(amountMinor int64, currency string) (usdMinor int64, rate float64, ok bool) {
	rate, ok = r.Rate(currency)
	if !ok {
		return 0, 0, false
	}
	usdMinor, ok = USDMinor(amountMinor, currency, rate)
	return usdMinor, rate, ok
}
