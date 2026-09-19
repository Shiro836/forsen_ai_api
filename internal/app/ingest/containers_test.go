//go:build integration

package ingest

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"app/db"
	"app/pkg/twitch"

	"github.com/google/uuid"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

const pgImage = "pg_uuidv7:latest"

// Channels busy enough to announce something within the window. Their ids are
// the room-id tag of their own chat.
var liveChannels = map[string]int{
	"jynxzi":       411377640,
	"summit1g":     26490481,
	"lirik":        23161357,
	"esfandtv":     38746172,
	"jasontheween": 107117952,
	"zackrawrr":    552120296,
	"pokimane":     44445592,
	"rubius":       39276140,
}

func startDB(t *testing.T) *db.DB {
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
	database, err := db.New(ctx, &db.Config{ConnStr: connStr})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)
	if err := database.ApplyMigrations(ctx, db.Migrations, db.MigrationsDir, nil); err != nil {
		t.Fatalf("pg migrations: %v", err)
	}
	return database
}

// TestContainersLiveChatEvents watches real Twitch chat: the cheers, resubs
// and watch streaks of busy channels have to become queue rows of their lane.
func TestContainersLiveChatEvents(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	database := startDB(t)
	users := make(map[uuid.UUID]string, len(liveChannels))
	for login, twitchUserID := range liveChannels {
		userID, err := database.UpsertUser(ctx, &db.User{TwitchLogin: login, TwitchUserID: twitchUserID})
		if err != nil {
			t.Fatal(err)
		}
		settings := &db.UserSettings{}
		for _, lane := range []db.MsgClass{db.MsgClassSub, db.MsgClassStreak, db.MsgClassRaid} {
			settings.SetLaneEnabled(lane, true)
		}
		if err := database.UpdateUserData(ctx, userID, settings); err != nil {
			t.Fatal(err)
		}
		if _, err := database.AutoGrantAccess(ctx, &db.User{TwitchLogin: login, TwitchUserID: twitchUserID}, db.PermissionStreamer); err != nil {
			t.Fatal(err)
		}
		users[userID] = login
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	service := NewService(logger, database, &twitch.Config{}, EventSubConfig{})
	go func() {
		if err := service.Run(ctx); err != nil {
			t.Error(err)
		}
	}()

	seen := make(map[db.MsgClass]int)
	for ctx.Err() == nil {
		for userID, login := range users {
			msgs, err := database.GetMessageUpdates(ctx, userID, 0)
			if err != nil || len(msgs) == 0 {
				continue
			}
			for _, msg := range msgs {
				lane, ok := msg.TwitchMessage.EventLane()
				if !ok {
					t.Fatalf("%s: %q has no lane, event %+v", login, msg.TwitchMessage.Message, msg.TwitchMessage.Event)
				}
				if msg.TwitchMessage.TwitchLogin == "" {
					t.Fatalf("%s: %s row has no viewer", login, lane)
				}
				seen[lane]++
				t.Logf("%s: %s by %s, event %+v, text %q",
					login, lane, msg.TwitchMessage.TwitchLogin, msg.TwitchMessage.Event, msg.TwitchMessage.Message)
			}
			if err := database.CleanQueue(ctx, false); err != nil {
				t.Fatal(err)
			}
		}
		// Cheers come from a chat message, the rest from a notice; both paths
		// are worth waiting for, but only what Twitch sends can be asserted.
		if seen[db.MsgClassSub]+seen[db.MsgClassStreak]+seen[db.MsgClassRaid] > 0 {
			break
		}
		time.Sleep(2 * time.Second)
	}

	if len(seen) == 0 {
		t.Skip("no channel announced anything in the window; nothing to check")
	}
	t.Logf("lanes seen: %v", seen)
}
