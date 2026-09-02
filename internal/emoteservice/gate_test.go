package emoteservice

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"app/pkg/oai"
)

func testGate(t *testing.T, handler http.HandlerFunc, fallback time.Duration) *GPUGate {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	gate := NewGPUGate(slog.New(slog.NewTextHandler(io.Discard, nil)), server.URL, fallback)
	gate.poll = 10 * time.Millisecond
	return gate
}

// slotsHandler answers busy for the first busyProbes requests, then idle.
func slotsHandler(probes *atomic.Int32, busyProbes int32) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		busy := probes.Add(1) <= busyProbes
		fmt.Fprintf(w, `[{"id":0,"is_processing":%t},{"id":1,"is_processing":false}]`, busy)
	}
}

// The gate exists so a classification never lands on a GPU that is answering a
// viewer, and so the queue resumes the moment one stops.
func TestGateWaitsForABusyServerThenDispatches(t *testing.T) {
	t.Parallel()

	var probes atomic.Int32
	gate := testGate(t, slotsHandler(&probes, 3), time.Hour)

	started := time.Now()
	require.NoError(t, gate.Wait(context.Background()))

	assert.Equal(t, int32(4), probes.Load(), "must re-check until idle, then dispatch on the idle reading")
	assert.GreaterOrEqual(t, time.Since(started), 3*gate.poll, "must have waited out the busy probes")
}

func TestGateDispatchesImmediatelyWhenIdle(t *testing.T) {
	t.Parallel()

	var probes atomic.Int32
	gate := testGate(t, slotsHandler(&probes, 0), time.Hour)

	started := time.Now()
	require.NoError(t, gate.Wait(context.Background()))

	assert.Equal(t, int32(1), probes.Load())
	assert.Less(t, time.Since(started), gate.poll, "an idle server must not cost a poll interval")
}

// An unreadable /slots must degrade to the old fixed pacing rather than stop the
// queue, and must not spin.
func TestGateFallsBackToTheIntervalWhenSlotsIsMissing(t *testing.T) {
	t.Parallel()

	var probes atomic.Int32
	fallback := 40 * time.Millisecond
	gate := testGate(t, func(w http.ResponseWriter, r *http.Request) {
		probes.Add(1)
		http.NotFound(w, r)
	}, fallback)

	started := time.Now()
	require.NoError(t, gate.Wait(context.Background()))

	assert.Equal(t, int32(1), probes.Load(), "a dead endpoint must be probed once per dispatch, not polled")
	assert.GreaterOrEqual(t, time.Since(started), fallback)
}

// A response that parses but lists nothing is a wrong URL, not an idle server:
// treating it as idle would silently disable the gate.
func TestGateTreatsAnEmptySlotListAsUnreadable(t *testing.T) {
	t.Parallel()

	fallback := 30 * time.Millisecond
	gate := testGate(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[]`)
	}, fallback)

	started := time.Now()
	require.NoError(t, gate.Wait(context.Background()))
	assert.GreaterOrEqual(t, time.Since(started), fallback)
}

func TestGateWarningIsThrottled(t *testing.T) {
	t.Parallel()

	var lines int
	gate := NewGPUGate(slog.New(slog.NewTextHandler(writerFunc(func(p []byte) (int, error) {
		lines++
		return len(p), nil
	}), nil)), "", time.Millisecond)

	for i := 0; i < 5; i++ {
		require.NoError(t, gate.Wait(context.Background()))
	}

	assert.Equal(t, 1, lines, "an unreachable endpoint must not log once per emote")
	assert.Equal(t, 4, gate.suppressed)
}

func TestGateWaitHonoursContextCancellation(t *testing.T) {
	t.Parallel()

	var probes atomic.Int32
	gate := testGate(t, slotsHandler(&probes, 1000), time.Hour)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	assert.ErrorIs(t, gate.Wait(ctx), context.DeadlineExceeded)
}

func TestDeriveSlotsURL(t *testing.T) {
	t.Parallel()

	for base, want := range map[string]string{
		"http://localhost:3334/v1":  "http://localhost:3334/slots",
		"http://localhost:3334/v1/": "http://localhost:3334/slots",
		"http://localhost:3334":     "http://localhost:3334/slots",
		"":                          "",
	} {
		assert.Equal(t, want, deriveSlotsURL(base), base)
	}
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

// The gate is only worth anything if the worker consults it before the vision
// request rather than around it.
func TestWorkerWaitsForIdleBeforeClassifying(t *testing.T) {
	t.Parallel()

	var probes atomic.Int32
	var dispatchedAfter int32
	gate := testGate(t, slotsHandler(&probes, 3), time.Hour)

	worker := &Worker{
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		gate:   gate,
		classifier: &Classifier{
			logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
			vision: visionFunc(func() string {
				dispatchedAfter = probes.Load()
				return `{"description": "a man doused in white cream", "classes": ["cum"]}`
			}),
		},
	}

	got, err := worker.visionClassify(context.Background(), "forsenCream", &Grid{PNG: []byte{1}, Tiles: 10})

	require.NoError(t, err)
	assert.Equal(t, []string{ClassCum}, got.Classes)
	assert.Equal(t, int32(4), dispatchedAfter, "the request must go out only after an idle reading")
}

type visionFunc func() string

func (f visionFunc) AskVisionJSON(context.Context, string, []oai.Image, json.RawMessage, float64) (string, error) {
	return f(), nil
}
