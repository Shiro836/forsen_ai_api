//go:build integration

package ingest

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"app/db"
	"app/pkg/twitch"

	"github.com/google/uuid"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

const pgImage = "pg_uuidv7:latest"

// liveChannels are the chats to watch, from LIVE_CHANNELS (comma-separated
// logins). A fixed list is asleep for most of the day, so the caller names
// whoever is live; the ids only have to differ from each other here.
func liveChannels(t *testing.T) map[string]int {
	t.Helper()
	logins := strings.FieldsFunc(os.Getenv("LIVE_CHANNELS"), func(r rune) bool { return r == ',' || r == '\n' || r == ' ' })
	if len(logins) == 0 {
		t.Skip("LIVE_CHANNELS is empty; name some channels that are live right now")
	}
	channels := make(map[string]int, len(logins))
	for i, login := range logins {
		channels[strings.ToLower(login)] = i + 1
	}
	return channels
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

// TestContainersBothFeedsMakeOneRow delivers each event the way both feeds do,
// in either order, and once more from a single feed: a second feed must never
// add a row, and a missing one must never lose it.
func TestContainersBothFeedsMakeOneRow(t *testing.T) {
	ctx := context.Background()
	database := startDB(t)

	userID, err := database.UpsertUser(ctx, &db.User{TwitchLogin: "streamer", TwitchUserID: 1})
	if err != nil {
		t.Fatal(err)
	}
	settings := &db.UserSettings{}
	for _, lane := range []db.MsgClass{db.MsgClassSub, db.MsgClassRaid} {
		settings.SetLaneEnabled(lane, true)
	}
	userCfg := &ingestUserConfig{id: userID, twitchUserID: 1, settings: *settings}

	service := NewService(slog.New(slog.NewTextHandler(io.Discard, nil)), database, &twitch.Config{}, EventSubConfig{})

	viewer := 100
	event := func(pairAs, chatText string, meta db.EventMeta) (fromChat, fromEvents arrival) {
		viewer++
		chatMeta, eventsMeta := meta, meta
		fromChat = arrival{
			msg:      db.TwitchMessage{TwitchLogin: "viewer", TwitchUserID: viewer, Message: chatText, Event: &chatMeta},
			uniqueID: uuid.NewString(),
			pairAs:   pairAs,
		}
		fromEvents = arrival{
			msg:      db.TwitchMessage{TwitchLogin: "viewer", TwitchUserID: viewer, Event: &eventsMeta},
			uniqueID: "eventsub:" + uuid.NewString(),
			pairAs:   pairAs,
		}
		return fromChat, fromEvents
	}

	rows := func() int {
		msgs, err := database.GetMessageUpdates(ctx, userID, 0)
		if err != nil {
			t.Fatal(err)
		}
		return len(msgs)
	}

	kinds := []struct {
		name   string
		pairAs string
		meta   db.EventMeta
	}{
		{"sub", pairAsSub, db.EventMeta{Kind: db.EventKindSub, Tier: 1}},
		{"resub", pairAsResub, db.EventMeta{Kind: db.EventKindResub, Tier: 1, Months: 6}},
		{"gift", pairAsGift(5), db.EventMeta{Kind: db.EventKindGiftSubs, Tier: 1, GiftCount: 5}},
		{"raid", pairAsRaid, db.EventMeta{Kind: db.EventKindRaid, Viewers: 91}},
	}

	want := 0
	for _, kind := range kinds {
		// Chat may carry words the event never has; that must not split the pair.
		fromChat, fromEvents := event(kind.pairAs, "words only chat has", kind.meta)
		service.push(ctx, "streamer", userCfg, fromChat, feedChat)
		service.push(ctx, "streamer", userCfg, fromEvents, feedEventSub)
		want++
		if got := rows(); got != want {
			t.Fatalf("%s, chat first: %d rows, want %d", kind.name, got, want)
		}

		fromChat, fromEvents = event(kind.pairAs, "", kind.meta)
		service.push(ctx, "streamer", userCfg, fromEvents, feedEventSub)
		service.push(ctx, "streamer", userCfg, fromChat, feedChat)
		want++
		if got := rows(); got != want {
			t.Fatalf("%s, events first: %d rows, want %d", kind.name, got, want)
		}

		fromChat, _ = event(kind.pairAs, "", kind.meta)
		service.push(ctx, "streamer", userCfg, fromChat, feedChat)
		want++
		if got := rows(); got != want {
			t.Fatalf("%s, chat alone: %d rows, want %d", kind.name, got, want)
		}

		_, fromEvents = event(kind.pairAs, "", kind.meta)
		service.push(ctx, "streamer", userCfg, fromEvents, feedEventSub)
		want++
		if got := rows(); got != want {
			t.Fatalf("%s, events alone: %d rows, want %d", kind.name, got, want)
		}
	}
}

// TestContainersLiveChatEvents watches real Twitch chat: the cheers, resubs
// and watch streaks of busy channels have to become queue rows of their lane.
func TestContainersLiveChatEvents(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	channels := liveChannels(t)
	database := startDB(t)
	users := make(map[uuid.UUID]string, len(channels))
	for login, twitchUserID := range channels {
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
	kinds := make(map[db.EventKind]int)
	counted := make(map[uuid.UUID]bool)
	for ctx.Err() == nil {
		for userID, login := range users {
			msgs, err := database.GetMessageUpdates(ctx, userID, 0)
			if err != nil || len(msgs) == 0 {
				continue
			}
			for _, msg := range msgs {
				if counted[msg.ID] {
					continue
				}
				counted[msg.ID] = true

				// These channels have rewards of their own; a redeem of one is
				// queued as unbound, as it is for any channel we ingest.
				if msg.TwitchMessage.RewardID != "" {
					continue
				}

				lane, ok := msg.TwitchMessage.EventLane()
				if !ok {
					t.Fatalf("%s: %q has no lane, event %+v", login, msg.TwitchMessage.Message, msg.TwitchMessage.Event)
				}
				if msg.TwitchMessage.TwitchLogin == "" {
					t.Fatalf("%s: %s row has no viewer", login, lane)
				}
				seen[lane]++
				kinds[msg.TwitchMessage.Event.Kind]++
				t.Logf("%s: %s, event %+v, typed %t", login, lane, *msg.TwitchMessage.Event, msg.TwitchMessage.Message != "")
			}
		}
		// Only what Twitch sends can be asserted, so this waits for a few
		// lanes and settles for whatever came by the deadline.
		if len(seen) >= 3 {
			break
		}
		time.Sleep(2 * time.Second)
	}

	if len(seen) == 0 {
		t.Skip("no channel announced anything in the window; nothing to check")
	}
	t.Logf("rows by lane: %v, by kind: %v", seen, kinds)
}
