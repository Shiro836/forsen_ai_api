//go:build integration

package db

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

func TestContainersRewardBindingsKeepPointsAndBits(t *testing.T) {
	ctx := context.Background()
	database := startPostgres(t)
	if err := database.ApplyMigrations(ctx, Migrations, MigrationsDir, nil); err != nil {
		t.Fatalf("migrations: %v", err)
	}

	var userID uuid.UUID
	if err := database.QueryRow(ctx, `
		INSERT INTO users (twitch_login, twitch_user_id, data)
		VALUES ('alice', 1, '{}') RETURNING id
	`).Scan(&userID); err != nil {
		t.Fatal(err)
	}

	var cardID uuid.UUID
	if err := database.QueryRow(ctx, `
		INSERT INTO char_cards (owner_user_id, name, description)
		VALUES ($1, 'card', '') RETURNING id
	`, userID).Scan(&cardID); err != nil {
		t.Fatal(err)
	}

	for _, id := range []string{"points-reward", "bits-power-up", "points-reward"} {
		if err := database.UpsertTwitchReward(ctx, userID, &cardID, id, TwitchRewardAI); err != nil {
			t.Fatalf("bind %s: %v", id, err)
		}
	}

	for _, id := range []string{"points-reward", "bits-power-up"} {
		card, rewardType, err := database.GetCharCardByTwitchRewardNoPerms(ctx, id)
		if err != nil {
			t.Fatalf("lookup %s: %v", id, err)
		}
		if card.ID != cardID || rewardType != TwitchRewardAI {
			t.Fatalf("lookup %s = card %s type %d", id, card.ID, rewardType)
		}
	}

	var characterBindings int
	if err := database.QueryRow(ctx, `
		SELECT count(*) FROM reward_buttons
		WHERE user_id = $1 AND card_id = $2 AND reward_type = $3
	`, userID, cardID, TwitchRewardAI).Scan(&characterBindings); err != nil {
		t.Fatal(err)
	}
	if characterBindings != 2 {
		t.Fatalf("character bindings = %d, want 2", characterBindings)
	}

	for _, id := range []string{"universal-points", "universal-bits", "universal-points"} {
		if err := database.UpsertUniversalTTSReward(ctx, userID, id); err != nil {
			t.Fatalf("bind %s: %v", id, err)
		}
	}

	for _, id := range []string{"universal-points", "universal-bits"} {
		cardID, rewardType, err := database.GetRewardByTwitchReward(ctx, id)
		if err != nil {
			t.Fatalf("lookup %s: %v", id, err)
		}
		if cardID != nil || rewardType != TwitchRewardUniversalTTS {
			t.Fatalf("lookup %s = card %v type %d", id, cardID, rewardType)
		}
	}

	var universalBindings int
	if err := database.QueryRow(ctx, `
		SELECT count(*) FROM reward_buttons
		WHERE user_id = $1 AND card_id IS NULL AND reward_type = $2
	`, userID, TwitchRewardUniversalTTS).Scan(&universalBindings); err != nil {
		t.Fatal(err)
	}
	if universalBindings != 2 {
		t.Fatalf("universal bindings = %d, want 2", universalBindings)
	}
}
