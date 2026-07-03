package audiotree

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path"
	"testing"
	"time"

	"app/pkg/ffmpeg"
)

type renderer interface {
	Render(ctx context.Context, segments []Segment, padding time.Duration, disableLimiter bool) ([]byte, []Placement, error)
}

func newTestRenderer(t *testing.T) (*Renderer, *ffmpeg.Client) {
	t.Helper()
	client := ffmpeg.New(&ffmpeg.Config{TmpDir: t.TempDir()})
	return New(client), client
}

// bothRenderers runs a test against the tree and legacy implementations,
// for behavior the two must agree on.
func bothRenderers(t *testing.T, test func(t *testing.T, r renderer, client *ffmpeg.Client)) {
	t.Helper()
	client := ffmpeg.New(&ffmpeg.Config{TmpDir: t.TempDir()})

	t.Run("tree", func(t *testing.T) { test(t, New(client), client) })
	t.Run("legacy", func(t *testing.T) { test(t, NewLegacy(client), client) })
}

func sine(t *testing.T, dur time.Duration) []byte {
	t.Helper()
	out := path.Join(t.TempDir(), "sine.wav")
	cmd := exec.Command("ffmpeg", "-nostats", "-loglevel", "error",
		"-f", "lavfi", "-i", fmt.Sprintf("sine=frequency=440:duration=%.3f", dur.Seconds()),
		"-ar", "44100", "-ac", "2", "-y", out)
	if err := cmd.Run(); err != nil {
		t.Fatalf("generate sine: %v", err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func wantDur(t *testing.T, got, want time.Duration) {
	t.Helper()
	diff := got - want
	if diff < 0 {
		diff = -diff
	}
	if diff > 200*time.Millisecond {
		t.Errorf("duration = %v, want %v ±200ms", got, want)
	}
}

func TestRenderConcatOnly(t *testing.T) {
	clip := sine(t, time.Second)

	bothRenderers(t, func(t *testing.T, r renderer, client *ffmpeg.Client) {
		ctx := context.Background()

		audio, placements, err := r.Render(ctx, []Segment{
			{Audio: clip, Duration: time.Second},
			{Audio: clip, Duration: time.Second},
		}, 500*time.Millisecond, false)
		if err != nil {
			t.Fatal(err)
		}

		probe, err := client.Ffprobe(ctx, audio)
		if err != nil {
			t.Fatal(err)
		}
		wantDur(t, probe.Duration, 2500*time.Millisecond)

		wantDur(t, placements[0].Start, 0)
		wantDur(t, placements[0].End, time.Second)
		wantDur(t, placements[1].Start, 1500*time.Millisecond)
		wantDur(t, placements[1].End, 2500*time.Millisecond)
	})
}

func TestRenderBackgroundSpansSegments(t *testing.T) {
	r, client := newTestRenderer(t)
	ctx := context.Background()
	clip := sine(t, time.Second)
	keyboard := fmt.Sprintf("%d", ffmpeg.FilterBackgroundKeyboard)

	audio, placements, err := r.Render(ctx, []Segment{
		{Audio: clip, Filters: []string{keyboard}, Duration: time.Second},
		{Audio: clip, Filters: []string{keyboard}, Duration: time.Second},
	}, 500*time.Millisecond, false)
	if err != nil {
		t.Fatal(err)
	}

	probe, err := client.Ffprobe(ctx, audio)
	if err != nil {
		t.Fatal(err)
	}
	wantDur(t, probe.Duration, 2500*time.Millisecond)
	wantDur(t, placements[1].End, 2500*time.Millisecond)
}

func TestRenderTempoScalesPlacements(t *testing.T) {
	clip := sine(t, time.Second)
	slower := fmt.Sprintf("%d", ffmpeg.FilterSlower)

	bothRenderers(t, func(t *testing.T, r renderer, client *ffmpeg.Client) {
		ctx := context.Background()

		audio, placements, err := r.Render(ctx, []Segment{
			{Audio: clip, Filters: []string{slower}, Duration: time.Second},
			{Audio: clip, Duration: time.Second},
		}, 500*time.Millisecond, false)
		if err != nil {
			t.Fatal(err)
		}

		stretched := time.Duration(durationMultiplier(ffmpeg.FilterSlower) * float64(time.Second))

		probe, err := client.Ffprobe(ctx, audio)
		if err != nil {
			t.Fatal(err)
		}
		wantDur(t, probe.Duration, stretched+500*time.Millisecond+time.Second)

		wantDur(t, placements[0].End, stretched)
		wantDur(t, placements[1].Start, stretched+500*time.Millisecond)
	})
}

func TestRenderNestedFiltersAndReverb(t *testing.T) {
	r, client := newTestRenderer(t)
	ctx := context.Background()
	clip := sine(t, time.Second)
	hall := fmt.Sprintf("%d", ffmpeg.FilterHallEcho)
	muffled := fmt.Sprintf("%d", ffmpeg.FilterMuffled)

	audio, placements, err := r.Render(ctx, []Segment{
		{Audio: clip, Filters: []string{hall, muffled}, Duration: time.Second},
		{Audio: clip, Filters: []string{hall}, Duration: time.Second},
	}, 500*time.Millisecond, false)
	if err != nil {
		t.Fatal(err)
	}

	probe, err := client.Ffprobe(ctx, audio)
	if err != nil {
		t.Fatal(err)
	}
	if probe.Duration < 2*time.Second {
		t.Errorf("duration = %v, want at least 2s", probe.Duration)
	}
	if placements[1].Start <= placements[0].Start {
		t.Errorf("placements out of order: %+v", placements)
	}
}

func TestRenderSweepUsesSpanDuration(t *testing.T) {
	r, client := newTestRenderer(t)
	ctx := context.Background()
	clip := sine(t, time.Second)
	leftToRight := fmt.Sprintf("%d", ffmpeg.FilterLeftToRight)

	audio, _, err := r.Render(ctx, []Segment{
		{Audio: clip, Filters: []string{leftToRight}, Duration: time.Second},
		{Audio: clip, Filters: []string{leftToRight}, Duration: time.Second},
	}, 500*time.Millisecond, false)
	if err != nil {
		t.Fatal(err)
	}

	probe, err := client.Ffprobe(ctx, audio)
	if err != nil {
		t.Fatal(err)
	}
	wantDur(t, probe.Duration, 2500*time.Millisecond)
}

func TestRenderSingleSegmentNoFilters(t *testing.T) {
	clip := sine(t, time.Second)

	bothRenderers(t, func(t *testing.T, r renderer, _ *ffmpeg.Client) {
		audio, placements, err := r.Render(context.Background(), []Segment{
			{Audio: clip, Duration: time.Second},
		}, 500*time.Millisecond, false)
		if err != nil {
			t.Fatal(err)
		}
		if len(audio) == 0 {
			t.Fatal("empty audio")
		}
		wantDur(t, placements[0].End, time.Second)
	})
}
