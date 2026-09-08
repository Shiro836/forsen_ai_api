//go:build integration

package db

import (
	"context"
	"errors"
	"slices"
	"testing"
)

func TestContainersTwitchTokensMigration(t *testing.T) {
	ctx := context.Background()
	database := startPostgres(t)

	if err := database.ApplyMigrations(ctx, migrationsBefore(t, "014_"), MigrationsDir, nil); err != nil {
		t.Fatalf("legacy migrations: %v", err)
	}
	if _, err := database.Exec(ctx, `
		INSERT INTO users (twitch_login, twitch_user_id, twitch_refresh_token, twitch_access_token)
		VALUES ('alice', 1, 'legacy-refresh', 'legacy-access')
	`); err != nil {
		t.Fatal(err)
	}

	if err := database.ApplyMigrations(ctx, Migrations, MigrationsDir, nil); err != nil {
		t.Fatalf("migrations: %v", err)
	}
	user, err := database.GetUserByTwitchLogin(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	tokens, err := database.GetTwitchTokens(ctx, user.ID)
	if err != nil {
		t.Fatalf("legacy tokens lost in migration: %v", err)
	}
	if tokens.AccessToken != "legacy-access" || tokens.RefreshToken != "legacy-refresh" || len(tokens.Scopes) != 0 {
		t.Fatalf("migrated tokens: %+v", tokens)
	}

	if err := database.RefreshTwitchTokens(ctx, user.ID, "new-access", "new-refresh"); err != nil {
		t.Fatal(err)
	}
	if err := database.ApplyMigrations(ctx, Migrations, MigrationsDir, nil); err != nil {
		t.Fatalf("re-applying migrations: %v", err)
	}
	tokens, err = database.GetTwitchTokens(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if tokens.AccessToken != "new-access" {
		t.Fatalf("re-apply must not restore stale tokens, got %q", tokens.AccessToken)
	}
}

func TestContainersTwitchTokensLifecycle(t *testing.T) {
	ctx := context.Background()
	database := startPostgres(t)
	if err := database.ApplyMigrations(ctx, Migrations, MigrationsDir, nil); err != nil {
		t.Fatalf("migrations: %v", err)
	}

	id, err := database.UpsertUser(ctx, &User{TwitchLogin: "bob", TwitchUserID: 2})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.GetTwitchTokens(ctx, id); !errors.Is(err, ErrNoRows) {
		t.Fatalf("user without tokens: want no rows, got %v", err)
	}

	scopes := []string{"channel:read:subscriptions", "channel:manage:redemptions"}
	if err := database.SetTwitchTokens(ctx, id, &TwitchTokens{AccessToken: "a1", RefreshToken: "r1", Scopes: scopes}); err != nil {
		t.Fatal(err)
	}
	if err := database.RefreshTwitchTokens(ctx, id, "a2", "r2"); err != nil {
		t.Fatal(err)
	}
	tokens, err := database.GetTwitchTokens(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if tokens.AccessToken != "a2" || tokens.RefreshToken != "r2" {
		t.Fatalf("refresh not stored: %+v", tokens)
	}
	if !slices.Equal(tokens.Scopes, scopes) {
		t.Fatalf("refresh must keep scopes, got %v", tokens.Scopes)
	}

	if _, err := database.Exec(ctx, `DELETE FROM users WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := database.GetTwitchTokens(ctx, id); !errors.Is(err, ErrNoRows) {
		t.Fatalf("tokens must go with the user, got %v", err)
	}
}
