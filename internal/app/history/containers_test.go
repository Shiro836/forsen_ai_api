//go:build integration

package history

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"testing"
	"time"

	"app/db"
	"app/pkg/archive"
	"app/pkg/clickhouse"

	chgo "github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/google/uuid"
	tcclickhouse "github.com/testcontainers/testcontainers-go/modules/clickhouse"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

// Images match the ones the services run on: pg_uuidv7 is the local postgres
// build with uuid_generate_v7, the ClickHouse tag is the compose pin.
const (
	pgImage = "pg_uuidv7:latest"
	chImage = "clickhouse/clickhouse-server:26.3.31"
)

func startStores(t *testing.T) (*db.DB, driver.Conn) {
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

	chc, err := tcclickhouse.Run(ctx, chImage,
		tcclickhouse.WithUsername("app"), tcclickhouse.WithPassword("pw"), tcclickhouse.WithDatabase("archive"))
	if err != nil {
		t.Skipf("clickhouse container: %v", err)
	}
	t.Cleanup(func() { _ = chc.Terminate(ctx) })
	host, err := chc.ConnectionHost(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cfg := clickhouse.Config{Addr: host, User: "app", Password: "pw", Database: "archive"}
	if err := clickhouse.Migrate(ctx, &cfg, db.ClickHouseMigrations, db.ClickHouseMigrationsDir); err != nil {
		t.Fatalf("clickhouse migrations: %v", err)
	}
	conn, err := clickhouse.Open(ctx, &cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return database, conn
}

func newUser(t *testing.T, database *db.DB, login string, twitchID int) uuid.UUID {
	t.Helper()
	id, err := database.UpsertUser(context.Background(), &db.User{
		TwitchLogin: login, TwitchUserID: twitchID,
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// playMessage walks a queue row through the writes the processor makes:
// push, current, data merge, archive, processed. Returns the message id.
func playMessage(t *testing.T, database *db.DB, userID uuid.UUID, msg db.TwitchMessage, reply string, arch *archive.MessageArchive) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	id, err := database.PushMsg(ctx, userID, msg, &db.MessageData{})
	if err != nil {
		t.Fatal(err)
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(database.UpdateMessageStatus(ctx, id, db.MsgStatusCurrent))
	if reply != "" {
		must(database.UpdateMessageData(ctx, id, &db.MessageData{AIResponse: reply}))
	}
	if arch != nil {
		must(database.UpdateMessageArchive(ctx, id, arch))
	}
	must(database.UpdateMessageStatus(ctx, id, db.MsgStatusProcessed))
	return id
}

func countFinal(t *testing.T, ch driver.Conn, table string) uint64 {
	t.Helper()
	var n uint64
	if err := ch.QueryRow(context.Background(), "select count() from "+table+" final").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestContainersExportAndHistory(t *testing.T) {
	database, ch := startStores(t)
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	streamer := newUser(t, database, "streamer", 100)
	other := newUser(t, database, "other", 200)

	cardID, err := database.InsertCharCard(ctx, &db.Card{OwnerUserID: streamer, Name: "Doctor", Data: &db.CardData{Name: "Doctor"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.UpsertTwitchReward(ctx, streamer, &cardID, "rw-ai", db.TwitchRewardAI); err != nil {
		t.Fatal(err)
	}
	if err := database.UpsertTwitchReward(ctx, other, nil, "rw-other-tts", db.TwitchRewardUniversalTTS); err != nil {
		t.Fatal(err)
	}

	played := &archive.MessageArchive{
		Handler: "AI", Outcome: archive.OutcomePlayed,
		StartedAt: time.Now().Add(-10 * time.Second).UnixMilli(), FinishedAt: time.Now().UnixMilli(),
		LLMCalls:  []archive.LLMCall{{Kind: "character", Model: "m", Endpoint: "chat", Request: json.RawMessage(`{"messages":[]}`), Response: "I am the doctor", PromptTok: 120, OutputTok: 9, LatencyMs: 800}},
		TTSTracks: []archive.TTSTrack{{Engine: "index", Text: "I am the doctor", AudioSec: 3.5, PlayedSec: 3.5, AudioKey: "k/" + uuid.NewString() + ".mp3"}},
		Filter:    []archive.FilterRun{{Target: archive.FilterTargetReply, LatencyMs: 400}},
	}
	aiMsg := playMessage(t, database, streamer, db.TwitchMessage{TwitchLogin: "viewer", TwitchUserID: 7, Message: "hello doctor", RewardID: "rw-ai"}, "I am the doctor", played)
	// a redeem of a reward this channel never bound: seen by ingest, never archived
	foreign := playMessage(t, database, streamer, db.TwitchMessage{TwitchLogin: "viewer", Message: "foreign reward", RewardID: "rw-someone-elses"}, "", nil)
	chatMsg := playMessage(t, database, streamer, db.TwitchMessage{TwitchLogin: "chatter", Message: "plain chat line", RewardID: ""}, "", &archive.MessageArchive{Handler: "chat_tts", Outcome: archive.OutcomeSilent})
	// the same reward id bound by another channel must not leak into this one
	otherMsg := playMessage(t, database, other, db.TwitchMessage{TwitchLogin: "viewer", Message: "other channel", RewardID: "rw-other-tts"}, "", nil)

	if n, err := database.CountUnexported(ctx); err != nil || n == 0 {
		t.Fatalf("unexported before tick = %d, %v", n, err)
	}

	exp := NewExporter(logger, database, ch)
	scanned, err := exp.Tick(ctx)
	if err != nil {
		t.Fatalf("tick: %v", err)
	}
	if scanned < 4 {
		t.Fatalf("tick scanned %d rows, want every version of the 4 messages", scanned)
	}
	if n, _ := database.CountUnexported(ctx); n != 0 {
		t.Fatalf("unexported after tick = %d", n)
	}

	if got := countFinal(t, ch, "messages"); got != 3 {
		t.Fatalf("messages final = %d, want 3 (ai, chat, other)", got)
	}
	if got := countFinal(t, ch, "llm_calls"); got != 1 {
		t.Errorf("llm_calls final = %d, want 1", got)
	}
	if got := countFinal(t, ch, "tts_tracks"); got != 1 {
		t.Errorf("tts_tracks final = %d, want 1", got)
	}
	if got := countFinal(t, ch, "filter_runs"); got != 1 {
		t.Errorf("filter_runs final = %d, want 1", got)
	}
	var foreignRows uint64
	if err := ch.QueryRow(ctx, "select count() from messages where id = {id:UUID}", chgo.Named("id", foreign.String())).Scan(&foreignRows); err != nil {
		t.Fatal(err)
	}
	if foreignRows != 0 {
		t.Errorf("foreign reward reached the archive")
	}

	reader := NewReader(database, ch)

	rows, err := reader.List(ctx, Query{ChannelID: streamer})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("streamer history has %d rows, want 2 (ai, chat): %+v", len(rows), rows)
	}
	if rows[0].ID != chatMsg || rows[1].ID != aiMsg {
		t.Errorf("order wrong: %v %v (chat was enqueued last)", rows[0].ID, rows[1].ID)
	}
	ai := ToItem(rows[1], ItemOptions{AudioURL: func(c, m, tr string) string { return c + "/" + m + "/" + tr }})
	if ai.Type != "AI" || ai.CharName != "Doctor" || ai.Outcome != "played" || ai.Status != "Processed" || ai.Response != "I am the doctor" {
		t.Errorf("ai item: %+v", ai)
	}
	if len(ai.Tracks) != 1 || ai.Tracks[0].AudioURL == "" || ai.LLM == nil || ai.LLM.PromptTokens != 120 {
		t.Errorf("ai item rollups: tracks=%+v llm=%+v", ai.Tracks, ai.LLM)
	}
	for _, r := range rows {
		if r.ID == otherMsg {
			t.Error("other channel's message leaked into streamer history")
		}
	}

	if rows, err := reader.List(ctx, Query{ChannelID: streamer, Search: "DOCTOR"}); err != nil || len(rows) != 1 || rows[0].ID != aiMsg {
		t.Errorf("search: %d rows, %v", len(rows), err)
	}
	if rows, err := reader.List(ctx, Query{ChannelID: streamer, Before: rows[0].EnqueuedAt}); err != nil || len(rows) != 1 || rows[0].ID != aiMsg {
		t.Errorf("before paging: %d rows, %v", len(rows), err)
	}
	if rows, err := reader.List(ctx, Query{ChannelLogin: "other"}); err != nil || len(rows) != 1 || rows[0].ID != otherMsg {
		t.Errorf("admin channel filter: %d rows, %v", len(rows), err)
	}
	if all, err := reader.List(ctx, Query{}); err != nil || len(all) != 3 {
		t.Errorf("admin all channels: %d rows, %v", len(all), err)
	}
	if chans, err := reader.Channels(ctx); err != nil || len(chans) != 2 {
		t.Errorf("channels: %v, %v", chans, err)
	}

	// a message queued after the last tick is served from the postgres tail
	queued, err := database.PushMsg(ctx, streamer, db.TwitchMessage{TwitchLogin: "late", Message: "still in queue", RewardID: "rw-ai"}, &db.MessageData{})
	if err != nil {
		t.Fatal(err)
	}
	rows, err = reader.List(ctx, Query{ChannelID: streamer})
	if err != nil || len(rows) != 3 || rows[0].ID != queued {
		t.Fatalf("tail merge: %d rows, %v", len(rows), err)
	}
	if it := ToItem(rows[0], ItemOptions{}); it.Status != "Wait" || it.Outcome != "" || it.Type != "AI" {
		t.Errorf("queued item: %+v", it)
	}

	// export the waiting version, then finish the message: the archive must end
	// up with both versions and the purge must wait for the second one
	if _, err := exp.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	// the purge never deletes past the watermark: push the sequence far ahead
	// so the ~200-update window no longer protects the row
	if err := database.UpdateMessageStatus(ctx, queued, db.MsgStatusProcessed); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(ctx, "select nextval('updated_seq') from generate_series(1, 250)"); err != nil {
		t.Fatal(err)
	}
	if err := database.CleanQueue(ctx, true); err != nil {
		t.Fatal(err)
	}
	if _, err := database.GetMessageByID(ctx, queued); err != nil {
		t.Fatalf("purge deleted an unexported row: %v", err)
	}
	if _, err := exp.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if err := database.CleanQueue(ctx, true); err != nil {
		t.Fatal(err)
	}
	if _, err := database.GetMessageByID(ctx, queued); err == nil {
		t.Error("purge kept a row the archive already acknowledged")
	}
	// the archive holds both versions the exporter saw (Wait, then Processed)
	// and FINAL serves the newest
	var versions uint64
	if err := ch.QueryRow(ctx, "select count() from messages where id = {id:UUID}", chgo.Named("id", queued.String())).Scan(&versions); err != nil {
		t.Fatal(err)
	}
	if versions != 2 {
		t.Errorf("queued message has %d versions in the archive, want 2", versions)
	}
	if rows, err := reader.List(ctx, Query{ChannelID: streamer}); err != nil || len(rows) != 3 || rows[0].ID != queued || rows[0].Status != int8(db.MsgStatusProcessed) {
		t.Errorf("after purge: %d rows, %v", len(rows), err)
	}
}
