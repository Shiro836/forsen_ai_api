//go:build integration

package db

import (
	"context"
	"errors"
	"io/fs"
	"testing"
	"testing/fstest"

	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

const pgImage = "pg_uuidv7:latest"

func startPostgres(t *testing.T) *DB {
	t.Helper()
	ctx := context.Background()

	pg, err := postgres.Run(ctx, pgImage,
		postgres.WithDatabase("test"), postgres.WithUsername("postgres"), postgres.WithPassword("postgres"),
		postgres.BasicWaitStrategies())
	if err != nil {
		t.Skipf("postgres container: %v", err)
	}
	t.Cleanup(func() { _ = pg.Terminate(ctx) })
	connStr, err := pg.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	database, err := New(ctx, &Config{ConnStr: connStr})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)
	return database
}

func migrationsBefore(t *testing.T, stop string) fs.FS {
	t.Helper()
	entries, err := fs.ReadDir(Migrations, MigrationsDir)
	if err != nil {
		t.Fatal(err)
	}
	out := fstest.MapFS{}
	for _, e := range entries {
		if e.Name() >= stop {
			continue
		}
		content, err := fs.ReadFile(Migrations, MigrationsDir+"/"+e.Name())
		if err != nil {
			t.Fatal(err)
		}
		out[MigrationsDir+"/"+e.Name()] = &fstest.MapFile{Data: content}
	}
	return out
}

func TestContainersSessionsMigration(t *testing.T) {
	ctx := context.Background()
	database := startPostgres(t)

	if err := database.ApplyMigrations(ctx, migrationsBefore(t, "013_"), MigrationsDir, nil); err != nil {
		t.Fatalf("legacy migrations: %v", err)
	}
	const legacySession = "legacy-browser-session"
	if _, err := database.Exec(ctx, `
		INSERT INTO users (twitch_login, twitch_user_id, twitch_refresh_token, twitch_access_token, session)
		VALUES ('alice', 1, 'r', 'a', $1)
	`, legacySession); err != nil {
		t.Fatal(err)
	}

	if err := database.ApplyMigrations(ctx, Migrations, MigrationsDir, nil); err != nil {
		t.Fatalf("migrations: %v", err)
	}
	user, err := database.GetUserBySession(ctx, legacySession)
	if err != nil {
		t.Fatalf("legacy session lost in migration: %v", err)
	}
	if user.TwitchLogin != "alice" {
		t.Fatalf("legacy session resolved to %q", user.TwitchLogin)
	}

	if err := database.DeleteSession(ctx, legacySession); err != nil {
		t.Fatal(err)
	}
	if err := database.ApplyMigrations(ctx, Migrations, MigrationsDir, nil); err != nil {
		t.Fatalf("re-applying migrations: %v", err)
	}
	if _, err := database.GetUserBySession(ctx, legacySession); !errors.Is(err, ErrNoRows) {
		t.Fatalf("deleted session after re-apply: want no rows, got %v", err)
	}
}

func TestContainersSessionsManyPerUser(t *testing.T) {
	ctx := context.Background()
	database := startPostgres(t)
	if err := database.ApplyMigrations(ctx, Migrations, MigrationsDir, nil); err != nil {
		t.Fatalf("migrations: %v", err)
	}

	id, err := database.UpsertUser(ctx, &User{TwitchLogin: "bob", TwitchUserID: 2})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.SetTwitchTokens(ctx, id, &TwitchTokens{AccessToken: "a1", RefreshToken: "r1", Scopes: []string{"s"}}); err != nil {
		t.Fatal(err)
	}
	first, err := database.CreateSession(ctx, id)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := database.UpsertUser(ctx, &User{TwitchLogin: "bob", TwitchUserID: 2}); err != nil {
		t.Fatal(err)
	}
	if err := database.SetTwitchTokens(ctx, id, &TwitchTokens{AccessToken: "a2", RefreshToken: "r2", Scopes: []string{"s"}}); err != nil {
		t.Fatal(err)
	}
	second, err := database.CreateSession(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("session ids collide")
	}

	for _, s := range []string{first, second} {
		user, err := database.GetUserBySession(ctx, s)
		if err != nil {
			t.Fatalf("session %s: %v", s, err)
		}
		if user.ID != id {
			t.Fatalf("session %s resolved to user %s", s, user.ID)
		}
	}
	tokens, err := database.GetTwitchTokens(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if tokens.AccessToken != "a2" {
		t.Fatalf("second login must replace the pair, got %q", tokens.AccessToken)
	}

	if err := database.DeleteSession(ctx, first); err != nil {
		t.Fatal(err)
	}
	if _, err := database.GetUserBySession(ctx, first); !errors.Is(err, ErrNoRows) {
		t.Fatalf("deleted session: want no rows, got %v", err)
	}
	if _, err := database.GetUserBySession(ctx, second); err != nil {
		t.Fatalf("logout of one session must not touch the other: %v", err)
	}

	if _, err := database.Exec(ctx, `UPDATE user_sessions SET expires_at = now() - interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.GetUserBySession(ctx, second); !errors.Is(err, ErrNoRows) {
		t.Fatalf("expired session: want no rows, got %v", err)
	}
}
