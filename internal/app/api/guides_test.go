package api

import (
	"app/db"
	"app/pkg/ctxstore"
	"log/slog"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestObsGuideSteps(t *testing.T) {
	t.Parallel()

	for path, step := range obsGuideSteps {
		t.Run(path, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest("GET", "https://example.com"+path, nil)
			req = req.WithContext(ctxstore.WithUser(req.Context(), &db.User{TwitchLogin: "streamer"}))

			body := string((&API{}).obsGuide(req))
			for _, want := range []string{step.Title, "Step " + strconv.Itoa(step.Number) + " of 5"} {
				if !strings.Contains(body, want) {
					t.Fatalf("response for %s does not contain %q", path, want)
				}
			}
			if path == "/guide/browser-properties" && !strings.Contains(body, "example.com/streamer") {
				t.Fatalf("response for %s does not contain overlay URL", path)
			}
		})
	}
}

func TestAudioGuideSteps(t *testing.T) {
	t.Parallel()

	for path, step := range audioGuideSteps {
		t.Run(path, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest("GET", "https://example.com"+path, nil)

			body := string((&API{}).audioGuide(req))
			for _, want := range []string{step.Title, "Step " + strconv.Itoa(step.Number) + " of 6"} {
				if !strings.Contains(body, want) {
					t.Fatalf("response for %s does not contain %q", path, want)
				}
			}
		})
	}
}

func TestBitsGuideSteps(t *testing.T) {
	t.Parallel()

	for path, step := range bitsGuideSteps {
		t.Run(path, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest("GET", "https://example.com"+path, nil)
			req = req.WithContext(ctxstore.WithUser(req.Context(), &db.User{TwitchLogin: "streamer"}))

			body := string((&API{}).bitsGuide(req))
			for _, want := range []string{step.Title, "Step " + strconv.Itoa(step.Number) + " of 4"} {
				if !strings.Contains(body, want) {
					t.Fatalf("response for %s does not contain %q", path, want)
				}
			}
			if path == "/guide/bits" && !strings.Contains(body, "https://dashboard.twitch.tv/u/streamer/viewer-rewards/channel-points/rewards") {
				t.Fatalf("response for %s does not contain the dashboard URL", path)
			}
			if path == "/guide/bits/reward-id" && !strings.Contains(body, "/guide/bits/detect") {
				t.Fatalf("response for %s does not poll the detector endpoint", path)
			}
			if path == "/guide/bits/connect" && !strings.Contains(body, "only an example") {
				t.Fatalf("response for %s does not flag the mockup reward id as an example", path)
			}
			copy := strings.ToLower(bitsGuideCopy(t, body))
			for _, banned := range []string{"pipeline", "queue", "backend", "ingest", "redemption routing", "message flow", "\u2014"} {
				if strings.Contains(copy, banned) {
					t.Fatalf("response for %s leaks the word %q into streamer-facing copy", path, banned)
				}
			}
		})
	}
}

// bitsGuideCopy carves out the instruction column, so the jargon sweep does not
// trip over Twitch's own wording reproduced inside the dashboard mockups.
func bitsGuideCopy(t *testing.T, body string) string {
	t.Helper()

	const (
		start = `<div class="mt-5 space-y-4 leading-relaxed">`
		end   = `<div class="mt-8 flex items-center gap-3 border-t`
	)

	from := strings.Index(body, start)
	to := strings.Index(body, end)
	if from < 0 || to < from {
		t.Fatalf("could not locate the instruction column in the rendered guide")
	}

	return body[from+len(start) : to]
}

type fakeIRCClient struct {
	stop sync.Once
	done chan struct{}
}

func (c *fakeIRCClient) Connect() error {
	<-c.done

	return nil
}

func (c *fakeIRCClient) Disconnect() error {
	c.stop.Do(func() { close(c.done) })

	return nil
}

func (c *fakeIRCClient) isDisconnected() bool {
	select {
	case <-c.done:
		return true
	default:
		return false
	}
}

func newTestBitsDetector(t *testing.T) (*bitsDetector, *[]*fakeIRCClient) {
	t.Helper()

	detector := newBitsDetector(slog.New(slog.DiscardHandler))

	clients := &[]*fakeIRCClient{}
	detector.newClient = func(login string, onReward func(seenReward)) ircWatchClient {
		client := &fakeIRCClient{done: make(chan struct{})}
		*clients = append(*clients, client)

		return client
	}

	t.Cleanup(func() {
		for _, client := range *clients {
			_ = client.Disconnect()
		}
	})

	return detector, clients
}

func TestBitsDetectorWatcher(t *testing.T) {
	t.Parallel()

	detector, clients := newTestBitsDetector(t)

	var emit func(seenReward)
	detector.newClient = func(login string, onReward func(seenReward)) ircWatchClient {
		emit = onReward
		client := &fakeIRCClient{done: make(chan struct{})}
		*clients = append(*clients, client)

		return client
	}

	userID := uuid.New()
	detector.Observe(userID, "Streamer")
	detector.Observe(userID, "streamer")

	if len(*clients) != 1 {
		t.Fatalf("repeated polls started %d clients, want 1", len(*clients))
	}

	now := time.Now()
	emit(seenReward{RewardID: "a", Message: "first", SeenAt: now.Add(-time.Minute)})
	emit(seenReward{RewardID: "b", Message: "second", SeenAt: now.Add(-time.Second)})
	emit(seenReward{RewardID: "a", Message: "again", SeenAt: now})

	seen := detector.Seen(userID)
	if len(seen) != 2 {
		t.Fatalf("got %d captured rewards, want 2 deduped by id: %+v", len(seen), seen)
	}
	if seen[0].RewardID != "a" || seen[0].Message != "again" {
		t.Fatalf("newest capture is %+v, want the second sighting of a", seen[0])
	}
	if seen[1].RewardID != "b" {
		t.Fatalf("second capture is %+v, want b", seen[1])
	}

	detector.lock.Lock()
	detector.watchers[userID].deadline = time.Now().Add(-time.Second)
	detector.reapLocked(time.Now())
	detector.lock.Unlock()

	if seen := detector.Seen(userID); seen != nil {
		t.Fatalf("stale watcher survived the reaper: %+v", seen)
	}
	if !(*clients)[0].isDisconnected() {
		t.Fatal("reaped watcher was not disconnected")
	}
}

func TestBitsDetectorWatcherCap(t *testing.T) {
	t.Parallel()

	detector, clients := newTestBitsDetector(t)

	for range bitsWatchMax + 4 {
		detector.Observe(uuid.New(), "streamer")
	}

	if len(*clients) != bitsWatchMax {
		t.Fatalf("started %d clients, want the cap of %d", len(*clients), bitsWatchMax)
	}
}

func TestBitsGuideRequiresUser(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest("GET", "https://example.com/guide/bits", nil)
	body := string((&API{}).bitsGuide(req))
	if !strings.Contains(body, "no user found") {
		t.Fatalf("response does not contain missing-user error: %s", body)
	}
}

func TestLegacyHome(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest("GET", "https://example.com/guide/legacy", nil)
	req = req.WithContext(ctxstore.WithUser(req.Context(), &db.User{TwitchLogin: "streamer"}))

	body := string((&API{}).legacyHome(req))
	for _, want := range []string{"Quickstart", "obs_script.lua", "example.com/streamer"} {
		if !strings.Contains(body, want) {
			t.Fatalf("legacy home does not contain %q", want)
		}
	}
}

func TestObsGuideRequiresUser(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest("GET", "https://example.com/", nil)
	body := string((&API{}).obsGuide(req))
	if !strings.Contains(body, "no user found") {
		t.Fatalf("response does not contain missing-user error: %s", body)
	}
}
