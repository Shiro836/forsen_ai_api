package emoteservice

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// slotsPoll is how often a busy server is re-checked. It is an idleness check
// rather than a rate limit, so it is short: the queue must resume the moment the
// GPU frees up.
const slotsPoll = 300 * time.Millisecond

// gateWarnEvery throttles the /slots warning. Unreachable means unreachable for
// every emote in the queue, and one line per emote would bury the log.
const gateWarnEvery = time.Minute

// GPUGate holds a classification back until llama-server reports every slot
// free, so live roleplay always wins the GPU the vision model shares. When
// /slots cannot be read it degrades to fixed-interval pacing instead of stopping
// the queue, and warns.
type GPUGate struct {
	logger   *slog.Logger
	client   *http.Client
	url      string
	poll     time.Duration
	fallback time.Duration

	mu         sync.Mutex
	lastWarn   time.Time
	suppressed int
}

func NewGPUGate(logger *slog.Logger, slotsURL string, fallback time.Duration) *GPUGate {
	return &GPUGate{
		logger:   logger,
		client:   &http.Client{Timeout: 2 * time.Second},
		url:      slotsURL,
		poll:     slotsPoll,
		fallback: fallback,
	}
}

// Wait blocks until the server is idle. It must be called before the caller's
// own request goes out: an in-flight request occupies a slot, so a gate that
// could see its own work would never observe an idle server again.
func (g *GPUGate) Wait(ctx context.Context) error {
	for {
		idle, err := g.idle(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			g.warn(err)
			return sleepCtx(ctx, g.fallback)
		}
		if idle {
			return nil
		}
		if err := sleepCtx(ctx, g.poll); err != nil {
			return err
		}
	}
}

// idle reports whether no slot is generating. A response listing no slots is an
// error rather than an idle server, so a wrong URL that happens to return JSON
// cannot silently disable the gate.
func (g *GPUGate) idle(ctx context.Context) (bool, error) {
	if g.url == "" {
		return false, fmt.Errorf("no slots url configured")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, g.url, nil)
	if err != nil {
		return false, fmt.Errorf("build slots request: %w", err)
	}
	resp, err := g.client.Do(req)
	if err != nil {
		return false, fmt.Errorf("fetch slots: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("fetch slots: status %s", resp.Status)
	}

	var slots []struct {
		IsProcessing bool `json:"is_processing"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&slots); err != nil {
		return false, fmt.Errorf("decode slots: %w", err)
	}
	if len(slots) == 0 {
		return false, fmt.Errorf("decode slots: response listed no slots")
	}

	for _, slot := range slots {
		if slot.IsProcessing {
			return false, nil
		}
	}
	return true, nil
}

func (g *GPUGate) warn(cause error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if !g.lastWarn.IsZero() && time.Since(g.lastWarn) < gateWarnEvery {
		g.suppressed++
		return
	}

	g.logger.Warn("llama-server slots unreadable, pacing classifications on the fallback interval",
		"err", cause, "url", g.url, "interval", g.fallback, "suppressed", g.suppressed)
	g.lastWarn = time.Now()
	g.suppressed = 0
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
