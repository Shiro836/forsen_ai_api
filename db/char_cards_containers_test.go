//go:build integration

package db

import (
	"context"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestContainersCharCardTrendingAndNoLimit100(t *testing.T) {
	ctx := context.Background()
	database := startPostgres(t)
	if err := database.ApplyMigrations(ctx, Migrations, MigrationsDir, nil); err != nil {
		t.Fatalf("migrations: %v", err)
	}

	var userID uuid.UUID
	if err := database.QueryRow(ctx, `
		INSERT INTO users (twitch_login, twitch_user_id, data)
		VALUES ('streamer', 1001, '{}') RETURNING id
	`).Scan(&userID); err != nil {
		t.Fatal(err)
	}

	insertCard := func(name, shortName, desc string) uuid.UUID {
		t.Helper()
		var id uuid.UUID
		if err := database.QueryRow(ctx, `
			INSERT INTO char_cards (owner_user_id, name, description, public, short_char_name)
			VALUES ($1, $2, $3, true, $4) RETURNING id
		`, userID, name, desc, shortName).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}

	hotID := insertCard("Okayeg", "tresh", "funny teammate")
	decayedID := insertCard("Bane", "foryou", "big guy")
	staleID := insertCard("Old Classic", "oldie", "ancient")

	// Live increments write both char_cards and today's char_card_daily_stats row.
	for range 3 {
		if err := database.IncrementCharRedeems(ctx, hotID); err != nil {
			t.Fatalf("increment ai: %v", err)
		}
	}
	for range 2 {
		if err := database.IncrementCharTTSRedeems(ctx, hotID); err != nil {
			t.Fatalf("increment tts: %v", err)
		}
	}

	// Upsert with a lower value must not clobber today's live increments.
	today := time.Now().UTC()
	if err := database.UpsertCharCardDailyStats(ctx, hotID, today, 1, 1); err != nil {
		t.Fatalf("upsert today: %v", err)
	}

	// 4 days ago = 1 half-life -> 8 redeems decays to ~4.0 (< hotID's 5.0).
	if err := database.UpsertCharCardDailyStats(ctx, decayedID, today.AddDate(0, 0, -4), 4, 4); err != nil {
		t.Fatalf("upsert decayed: %v", err)
	}
	// 20 days ago = outside 14-day window -> 0 trending score.
	if err := database.UpsertCharCardDailyStats(ctx, staleID, today.AddDate(0, 0, -20), 50, 50); err != nil {
		t.Fatalf("upsert stale: %v", err)
	}

	voices, err := database.GetPublicShortNamedCards(ctx)
	if err != nil {
		t.Fatalf("GetPublicShortNamedCards: %v", err)
	}
	if len(voices) != 3 {
		t.Fatalf("got %d voices, want 3", len(voices))
	}
	if voices[0].ShortCharName != "tresh" || voices[1].ShortCharName != "foryou" || voices[2].ShortCharName != "oldie" {
		t.Fatalf("unexpected trending order: %+v", voices)
	}
	if voices[0].Name != "Okayeg" || voices[0].Description != "funny teammate" || voices[0].Redeems != 3 || voices[0].TTSRedeems != 2 {
		t.Fatalf("unexpected hot voice metadata: %+v", voices[0])
	}
	// All 3 test cards were just created (age ~0), so each gets the same
	// freshness anchor on top of its 14-day decayed usage (5.0, 4.0, 0.0).
	if math.Abs((voices[0].TrendingScore-voices[2].TrendingScore)-5.0) > 0.05 {
		t.Fatalf("hot - stale delta = %f, want ~5.0", voices[0].TrendingScore-voices[2].TrendingScore)
	}
	if math.Abs((voices[1].TrendingScore-voices[2].TrendingScore)-4.0) > 0.05 {
		t.Fatalf("decayed - stale delta = %f, want ~4.0", voices[1].TrendingScore-voices[2].TrendingScore)
	}

	// Ensure GetCharCards is no longer truncated at 100 rows.
	for i := range 105 {
		insertCard(fmt.Sprintf("Extra %03d", i), fmt.Sprintf("extra%03d", i), "")
	}
	cards, err := database.GetCharCards(ctx, userID, GetChatCardsParams{ShowPublic: true, SortBy: SortByNewest})
	if err != nil {
		t.Fatalf("GetCharCards: %v", err)
	}
	if len(cards) != 108 {
		t.Fatalf("GetCharCards returned %d cards, want 108 (no LIMIT 100 truncation)", len(cards))
	}
}
