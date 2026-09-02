package api

import (
	"log/slog"
	"strings"
	"sync"
	"time"

	gempir "github.com/gempir/go-twitch-irc/v4"
	"github.com/google/uuid"
)

const (
	bitsWatchTTL      = 2 * time.Minute
	bitsWatchMax      = 16
	bitsWatchRing     = 10
	bitsWatchReapTick = 15 * time.Second
)

type seenReward struct {
	RewardID string
	Message  string
	SeenAt   time.Time
}

// ircWatchClient is the slice of the anonymous Twitch IRC client the detector
// needs. Tests substitute a client that never dials.
type ircWatchClient interface {
	Connect() error
	Disconnect() error
}

type bitsWatcher struct {
	client   ircWatchClient
	login    string
	deadline time.Time
	seen     []seenReward
}

// bitsDetector joins a streamer's chat anonymously while they sit on the
// reward-id step of the bits guide. twitch-ingest only joins channels that
// already have a reward button, so a streamer creating their very first reward
// would otherwise never have their test redemption picked up anywhere.
type bitsDetector struct {
	logger    *slog.Logger
	newClient func(login string, onReward func(seenReward)) ircWatchClient

	lock     sync.Mutex
	watchers map[uuid.UUID]*bitsWatcher
	reaping  bool
}

func newBitsDetector(logger *slog.Logger) *bitsDetector {
	return &bitsDetector{
		logger:    logger,
		newClient: newAnonymousRewardClient,
		watchers:  make(map[uuid.UUID]*bitsWatcher),
	}
}

func newAnonymousRewardClient(login string, onReward func(seenReward)) ircWatchClient {
	client := gempir.NewAnonymousClient()

	client.OnPrivateMessage(func(msg gempir.PrivateMessage) {
		if msg.CustomRewardID == "" {
			return
		}

		onReward(seenReward{
			RewardID: msg.CustomRewardID,
			Message:  msg.Message,
			SeenAt:   time.Now(),
		})
	})

	client.Join(login)

	return client
}

// Observe keeps a watcher on the user's channel alive for another bitsWatchTTL,
// starting one if the detector is below its watcher cap.
func (d *bitsDetector) Observe(userID uuid.UUID, login string) {
	login = strings.ToLower(strings.TrimSpace(login))
	if login == "" {
		return
	}

	d.lock.Lock()
	defer d.lock.Unlock()

	now := time.Now()
	d.reapLocked(now)

	if watcher, ok := d.watchers[userID]; ok {
		watcher.deadline = now.Add(bitsWatchTTL)

		return
	}

	if len(d.watchers) >= bitsWatchMax {
		d.logger.Warn("bits reward detector at capacity, serving stored reward ids only", "channel", login, "watchers", len(d.watchers))

		return
	}

	watcher := &bitsWatcher{
		login:    login,
		deadline: now.Add(bitsWatchTTL),
	}
	watcher.client = d.newClient(login, func(reward seenReward) {
		d.record(userID, reward)
	})

	d.watchers[userID] = watcher

	go d.run(userID, watcher)

	if !d.reaping {
		d.reaping = true

		go d.reapLoop()
	}
}

// Seen returns the reward redemptions captured live for the user, newest first.
func (d *bitsDetector) Seen(userID uuid.UUID) []seenReward {
	d.lock.Lock()
	defer d.lock.Unlock()

	watcher, ok := d.watchers[userID]
	if !ok {
		return nil
	}

	return append([]seenReward(nil), watcher.seen...)
}

func (d *bitsDetector) run(userID uuid.UUID, watcher *bitsWatcher) {
	if err := watcher.client.Connect(); err != nil {
		d.logger.Error("bits reward detector watcher stopped", "error", err, "channel", watcher.login)
	}

	d.lock.Lock()
	defer d.lock.Unlock()

	if d.watchers[userID] == watcher {
		delete(d.watchers, userID)
	}
}

func (d *bitsDetector) record(userID uuid.UUID, reward seenReward) {
	d.lock.Lock()
	defer d.lock.Unlock()

	watcher, ok := d.watchers[userID]
	if !ok {
		return
	}

	seen := make([]seenReward, 0, bitsWatchRing)
	seen = append(seen, reward)

	for _, old := range watcher.seen {
		if len(seen) >= bitsWatchRing {
			break
		}

		if old.RewardID == reward.RewardID {
			continue
		}

		seen = append(seen, old)
	}

	watcher.seen = seen
}

func (d *bitsDetector) reapLoop() {
	ticker := time.NewTicker(bitsWatchReapTick)
	defer ticker.Stop()

	for range ticker.C {
		d.lock.Lock()
		d.reapLocked(time.Now())
		empty := len(d.watchers) == 0
		if empty {
			d.reaping = false
		}
		d.lock.Unlock()

		if empty {
			return
		}
	}
}

func (d *bitsDetector) reapLocked(now time.Time) {
	for userID, watcher := range d.watchers {
		if now.Before(watcher.deadline) {
			continue
		}

		delete(d.watchers, userID)

		if err := watcher.client.Disconnect(); err != nil {
			d.logger.Debug("bits reward detector watcher already disconnected", "error", err, "channel", watcher.login)
		}
	}
}
