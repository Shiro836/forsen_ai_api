//go:build integration

package queue

import (
	"context"
	"errors"
	"testing"
	"time"

	"app/db"

	"github.com/google/uuid"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

const pgImage = "pg_uuidv7:latest"

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
	t      *testing.T
	db     *db.DB
	q      *Queue
	user   uuid.UUID
	reward string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	database := startDB(t)
	user, err := database.UpsertUser(ctx, &db.User{TwitchLogin: "streamer", TwitchUserID: 1})
	if err != nil {
		t.Fatal(err)
	}
	const reward = "rw-bound"
	if err := database.UpsertTwitchReward(ctx, user, nil, reward, db.TwitchRewardUniversalTTS); err != nil {
		t.Fatal(err)
	}
	return &fixture{t: t, db: database, q: New(database), user: user, reward: reward}
}

func (f *fixture) push(login, text, rewardID string) uuid.UUID {
	f.t.Helper()
	id, err := f.db.PushMsg(context.Background(), f.user, db.TwitchMessage{TwitchLogin: login, Message: text, RewardID: rewardID}, &db.MessageData{})
	if err != nil {
		f.t.Fatal(err)
	}
	return id
}

func (f *fixture) status(id uuid.UUID) db.MsgStatus {
	f.t.Helper()
	msg, err := f.db.GetMessageByID(context.Background(), id)
	if err != nil {
		f.t.Fatal(err)
	}
	return msg.Status
}

var rewardFirst = Order{db.MsgClassReward}

func (f *fixture) claim() (*Claimed, int) {
	f.t.Helper()
	return f.claimWith(rewardFirst)
}

func (f *fixture) claimWith(order Order) (*Claimed, int) {
	f.t.Helper()
	msg, purged, err := f.q.Claim(context.Background(), f.user, order)
	if err != nil {
		f.t.Fatalf("claim: %v", err)
	}
	return msg, purged
}

func (f *fixture) claimEmpty() {
	f.t.Helper()
	if _, _, err := f.q.Claim(context.Background(), f.user, rewardFirst); !errors.Is(err, ErrEmpty) {
		f.t.Fatalf("expected empty queue, got %v", err)
	}
}

func TestContainersOrderDecidesThePick(t *testing.T) {
	f := newFixture(t)
	reward := f.push("a", "redeem", f.reward)
	unbound := f.push("b", "other reward", "rw-unknown")

	msg, _ := f.claimWith(Order{db.MsgClassUnrouted, db.MsgClassReward})
	if msg.ID != unbound {
		t.Fatalf("claimed %v, want the later row whose class the order ranks first", msg.ID)
	}
	if err := f.q.Complete(context.Background(), unbound); err != nil {
		t.Fatal(err)
	}
	if msg, _ := f.claimWith(Order{db.MsgClassUnrouted, db.MsgClassReward}); msg.ID != reward {
		t.Fatalf("claimed %v, want the reward row", msg.ID)
	}
}

func TestContainersEmptyOrderIsArrivalOrder(t *testing.T) {
	f := newFixture(t)
	chat := f.push("a", "hi", "")
	f.push("b", "redeem", f.reward)

	msg, purged := f.claimWith(nil)
	if msg.ID != chat || purged != 0 {
		t.Fatalf("claimed %v purged=%d, want the first-arrived chat row and nothing purged", msg.ID, purged)
	}
}

func TestContainersRewardOutranksChat(t *testing.T) {
	f := newFixture(t)
	chatA := f.push("a", "hi", "")
	chatB := f.push("b", "hello", "")
	reward := f.push("c", "redeem", f.reward)

	msg, purged := f.claim()
	if msg.ID != reward || msg.Class != db.MsgClassReward {
		t.Fatalf("claimed %v class=%s, want reward row", msg.ID, msg.Class)
	}
	if purged != 2 {
		t.Fatalf("purged %d chat rows, want 2", purged)
	}
	for _, id := range []uuid.UUID{chatA, chatB} {
		if s := f.status(id); s != db.MsgStatusDeleted {
			t.Fatalf("chat row %v status %s, want Deleted", id, s)
		}
	}
	if s := f.status(reward); s != db.MsgStatusCurrent {
		t.Fatalf("reward status %s, want Current", s)
	}
	f.claimEmpty()
}

func TestContainersEqualRankPlaysInArrivalOrder(t *testing.T) {
	f := newFixture(t)
	unbound := f.push("a", "other reward", "rw-unknown")
	chat := f.push("b", "hi", "")

	msg, purged := f.claim()
	if msg.ID != unbound || msg.Class != db.MsgClassUnrouted || purged != 0 {
		t.Fatalf("claimed %v class=%s purged=%d, want unrouted row first with nothing purged", msg.ID, msg.Class, purged)
	}
	if err := f.q.Complete(context.Background(), unbound); err != nil {
		t.Fatal(err)
	}
	msg, purged = f.claim()
	if msg.ID != chat || msg.Class != db.MsgClassChat || purged != 0 {
		t.Fatalf("claimed %v class=%s purged=%d, want chat row", msg.ID, msg.Class, purged)
	}
}

func TestContainersDataUpdateKeepsOrder(t *testing.T) {
	f := newFixture(t)
	first := f.push("a", "first", "")
	f.push("b", "second", "")
	shown := true
	if err := f.db.UpdateMessageData(context.Background(), first, &db.MessageData{ShowImages: &shown}); err != nil {
		t.Fatal(err)
	}
	if msg, _ := f.claim(); msg.ID != first {
		t.Fatalf("claimed %v, want the first-arrived row", msg.ID)
	}
}

func TestContainersCompleteAndSkip(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	played := f.push("a", "played", f.reward)
	skipped := f.push("b", "skipped", f.reward)

	msg, _ := f.claim()
	if msg.ID != played {
		t.Fatalf("claimed %v, want %v", msg.ID, played)
	}
	if err := f.q.Complete(ctx, played); err != nil {
		t.Fatal(err)
	}
	if s := f.status(played); s != db.MsgStatusProcessed {
		t.Fatalf("status %s, want Processed", s)
	}

	f.claim()
	if err := f.q.Skip(ctx, skipped); err != nil {
		t.Fatal(err)
	}
	if err := f.q.Complete(ctx, skipped); err != nil {
		t.Fatal(err)
	}
	if s := f.status(skipped); s != db.MsgStatusDeleted {
		t.Fatalf("status %s after skip+complete, want Deleted", s)
	}
}

func TestContainersRecover(t *testing.T) {
	f := newFixture(t)
	id := f.push("a", "left current", f.reward)
	f.claim()

	n, err := f.q.Recover(context.Background(), f.user)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 || f.status(id) != db.MsgStatusProcessed {
		t.Fatalf("recovered %d, status %s; want 1 row Processed", n, f.status(id))
	}
}

func TestContainersPreempt(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	f.push("a", "chat plays", "")
	chat, _ := f.claim()
	preempt := f.q.WatchPreempt(ctx, chat, rewardFirst)

	select {
	case <-preempt:
		t.Fatal("preempted with nothing queued")
	case <-time.After(2 * watchInterval):
	}

	f.push("b", "redeem", f.reward)
	select {
	case <-preempt:
	case <-time.After(3 * watchInterval):
		t.Fatal("chat was not preempted by a queued reward")
	}
}

func TestContainersRewardNeverPreempted(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	f.push("a", "redeem plays", f.reward)
	reward, _ := f.claim()
	preempt := f.q.WatchPreempt(ctx, reward, rewardFirst)
	f.push("b", "another redeem", f.reward)

	select {
	case <-preempt:
		t.Fatal("reward row was preempted")
	case <-time.After(3 * watchInterval):
	}
}
