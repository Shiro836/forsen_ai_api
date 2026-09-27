//go:build integration

package ingest

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sort"
	"strings"
	"sync"
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

type fixture struct {
	t       *testing.T
	db      *db.DB
	service *Service
	userID  uuid.UUID
}

const streamerLogin = "streamer"

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	database := startDB(t)

	userID, err := database.UpsertUser(ctx, &db.User{TwitchLogin: streamerLogin, TwitchUserID: 1})
	if err != nil {
		t.Fatal(err)
	}
	settings := db.UserSettings{}
	for _, lane := range []db.MsgClass{db.MsgClassSub, db.MsgClassRaid, db.MsgClassChat} {
		settings.SetLaneEnabled(lane, true)
	}

	service := NewService(slog.New(slog.NewTextHandler(io.Discard, nil)), database, &twitch.Config{}, EventSubConfig{})
	service.activeUsers[streamerLogin] = &ingestUserConfig{id: userID, twitchUserID: 1, settings: settings}

	return &fixture{t: t, db: database, service: service, userID: userID}
}

func (f *fixture) rows() []*db.Message {
	f.t.Helper()
	msgs, err := f.db.GetMessageUpdates(context.Background(), f.userID, 0)
	if err != nil {
		f.t.Fatal(err)
	}
	sort.Slice(msgs, func(i, j int) bool { return msgs[i].ID.String() < msgs[j].ID.String() })
	return msgs
}

func redeemLine(id, text string) chatLine {
	return chatLine{channel: streamerLogin, id: id, viewerID: 7, viewer: "viewer", text: text, rewardID: "reward"}
}

func redemptionOf(id, text string) *redemption {
	return &redemption{
		broadcasterLogin: streamerLogin,
		viewerID:         7,
		rewardID:         "reward",
		text:             text,
		event:            db.EventMeta{Kind: db.EventKindCustomPowerUp, RedemptionID: id, Bits: 10, USD: 0.1},
	}
}

func TestContainersOneMessageIsOneRow(t *testing.T) {
	f := newFixture(t)

	for name, order := range map[string][]feed{
		"chat first":   {feedChat, feedEventSub},
		"events first": {feedEventSub, feedChat},
		"redelivered":  {feedChat, feedEventSub, feedEventSub, feedChat},
		"chat alone":   {feedChat},
		"events alone": {feedEventSub},
	} {
		before := len(f.rows())
		id := uuid.NewString()
		for _, from := range order {
			f.service.handleChatLine(chatLine{channel: streamerLogin, id: id, viewerID: 7, viewer: "viewer", text: "same words every time"}, from)
		}
		if got := len(f.rows()) - before; got != 1 {
			t.Fatalf("%s: %d rows, want 1", name, got)
		}
	}

	before := len(f.rows())
	id := uuid.NewString()
	var wg sync.WaitGroup
	for i := range 40 {
		wg.Go(func() {
			from := feedChat
			if i%2 == 1 {
				from = feedEventSub
			}
			f.service.handleChatLine(redeemLine(id, "raced"), from)
		})
	}
	wg.Wait()
	if got := len(f.rows()) - before; got != 1 {
		t.Fatalf("both feeds at once: %d rows, want 1", got)
	}
}

func TestContainersNoticeFromBothFeedsIsOneRow(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	userCfg, _ := f.service.activeUser(streamerLogin)

	in := arrival{
		msg:      db.TwitchMessage{TwitchLogin: "viewer", TwitchUserID: 7, Event: &db.EventMeta{Kind: db.EventKindSub, Tier: 1}},
		uniqueID: uuid.NewString(),
	}
	f.service.push(ctx, streamerLogin, userCfg, in, feedChat)
	f.service.push(ctx, streamerLogin, userCfg, in, feedEventSub)

	if got := len(f.rows()); got != 1 {
		t.Fatalf("%d rows, want 1", got)
	}
}

func TestContainersRedemptionFindsItsMessage(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	f.service.attachRedemption(ctx, redemptionOf("too-early", "nothing queued yet"))
	if got := len(f.rows()); got != 0 {
		t.Fatalf("a redemption made %d rows; it is not a message", got)
	}

	// What chat clients append to get past the duplicate-message check never
	// reaches the redemption's text.
	messageID := uuid.NewString()
	f.service.handleChatLine(redeemLine(messageID, "hello \U000E0000"), feedChat)
	f.service.attachRedemption(ctx, redemptionOf("r-1", "hello"))

	gotRedemption := func() {
		t.Helper()
		rows := f.rows()
		if len(rows) != 1 {
			t.Fatalf("%d rows, want 1", len(rows))
		}
		event := rows[0].TwitchMessage.Event
		if event == nil || event.RedemptionID != "r-1" || event.Bits != 10 || event.USD != 0.1 {
			t.Fatalf("message did not keep its redemption: %+v", event)
		}
	}
	gotRedemption()

	// The other feed's copy of the message comes after the redemption here.
	f.service.handleChatLine(redeemLine(messageID, "hello"), feedEventSub)
	gotRedemption()

	f.service.handleChatLine(redeemLine(uuid.NewString(), "hello"), feedChat)
	f.service.attachRedemption(ctx, redemptionOf("r-1", "hello"))
	if event := f.rows()[1].TwitchMessage.Event; event != nil {
		t.Fatalf("a redelivered redemption landed on the next identical message: %+v", event)
	}
}

func TestContainersIdenticalRedeemsEachGetTheirOwnRedemption(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	const redeems = 100

	for i := range redeems {
		f.service.handleChatLine(redeemLine(uuid.NewString(), "spam"), feedChat)
		// The event feed trails chat, so some redemptions find several
		// identical messages waiting.
		if i%3 == 2 {
			for j := i - 2; j <= i; j++ {
				f.service.attachRedemption(ctx, redemptionOf(fmt.Sprintf("r-%03d", j), "spam"))
			}
		}
	}
	f.service.attachRedemption(ctx, redemptionOf(fmt.Sprintf("r-%03d", redeems-1), "spam"))

	rows := f.rows()
	if len(rows) != redeems {
		t.Fatalf("%d rows, want %d", len(rows), redeems)
	}
	for i, row := range rows {
		want := fmt.Sprintf("r-%03d", i)
		if event := row.TwitchMessage.Event; event == nil || event.RedemptionID != want {
			t.Fatalf("message %d carries %+v, want redemption %s", i, event, want)
		}
	}
}

func TestContainersFinishedMessageTakesNoRedemption(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	f.service.handleChatLine(redeemLine(uuid.NewString(), "again"), feedChat)
	played := f.rows()[0]
	if err := f.db.UpdateMessageStatus(ctx, played.ID, db.MsgStatusProcessed); err != nil {
		t.Fatal(err)
	}

	f.service.handleChatLine(redeemLine(uuid.NewString(), "again"), feedChat)
	f.service.attachRedemption(ctx, redemptionOf("r-new", "again"))

	rows := f.rows()
	if event := rows[0].TwitchMessage.Event; event != nil {
		t.Fatalf("a message that already played took the redemption of a later one: %+v", event)
	}
	if event := rows[1].TwitchMessage.Event; event == nil || event.RedemptionID != "r-new" {
		t.Fatalf("the waiting message did not get its redemption: %+v", event)
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
