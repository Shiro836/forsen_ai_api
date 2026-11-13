//go:build integration

package db

import (
	"context"
	"testing"
	"time"
)

func TestContainersDonationRates(t *testing.T) {
	ctx := context.Background()
	database := startPostgres(t)
	if err := database.ApplyMigrations(ctx, Migrations, MigrationsDir, nil); err != nil {
		t.Fatalf("migrations: %v", err)
	}

	rates, err := database.GetDonationRates(ctx)
	if err != nil {
		t.Fatalf("an empty table must not be an error: %v", err)
	}
	if rates != nil {
		t.Fatalf("rates = %+v", rates)
	}

	want := map[string]float64{"USD": 1, "RUB": 80}
	fetched := time.Now().UTC().Truncate(time.Second)
	if err := database.SetDonationRates(ctx, want, fetched); err != nil {
		t.Fatal(err)
	}

	got, err := database.GetDonationRates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.Rates["RUB"] != 80 || !got.FetchedAt.Equal(fetched) {
		t.Fatalf("rates = %+v", got)
	}
}
