//go:build integration

package db

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

func TestContainersSetCardDisabled(t *testing.T) {
	ctx := context.Background()
	database := startPostgres(t)
	if err := database.ApplyMigrations(ctx, Migrations, MigrationsDir, nil); err != nil {
		t.Fatalf("migrations: %v", err)
	}

	var userID uuid.UUID
	if err := database.QueryRow(ctx, `
		INSERT INTO users (twitch_login, twitch_user_id, twitch_refresh_token, twitch_access_token, data)
		VALUES ('alice', 1, 'r', 'a', '{"filters": "badword"}') RETURNING id
	`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	cardA, cardB := uuid.New(), uuid.New()

	if err := database.SetCardDisabled(ctx, userID, cardA, true); err != nil {
		t.Fatal(err)
	}
	if err := database.SetCardDisabled(ctx, userID, cardA, true); err != nil {
		t.Fatal(err)
	}
	if err := database.SetCardDisabled(ctx, userID, cardB, true); err != nil {
		t.Fatal(err)
	}
	settings, err := database.GetUserSettings(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	if len(settings.DisabledCardIDs) != 2 || !settings.CardDisabled(cardA) || !settings.CardDisabled(cardB) {
		t.Fatalf("disabled = %v", settings.DisabledCardIDs)
	}
	if settings.Filters != "badword" {
		t.Fatalf("other settings lost: filters = %q", settings.Filters)
	}

	if err := database.SetCardDisabled(ctx, userID, cardA, false); err != nil {
		t.Fatal(err)
	}
	settings, err = database.GetUserSettings(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	if settings.CardDisabled(cardA) || !settings.CardDisabled(cardB) {
		t.Fatalf("after re-enabling A: %v", settings.DisabledCardIDs)
	}
}
