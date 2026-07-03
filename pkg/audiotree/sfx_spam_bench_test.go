package audiotree

import (
	"context"
	"os"
	"testing"
	"time"

	"app/pkg/ffmpeg"
)

// The prod message {2}{24}{2}{24}x7{3}x8{10}x8{12}x6 [344]{.}{.}[344]{.}[344]
// {.}[344]{.}[344]{.}[344]: six SFX sharing a deep filter prefix, with the
// {12} tail popped one by one. Stacks below are what limitFilters leaves.
func TestSfxSpamMessagePerformance(t *testing.T) {
	if testing.Short() {
		t.Skip("comparison benchmark")
	}

	sfx, err := os.ReadFile("../../internal/app/processor/sfx/344.mp3")
	if err != nil {
		t.Fatal(err)
	}
	sfxDur := 5499 * time.Millisecond

	prefix := []string{"2", "24", "2", "24", "24", "3", "3", "3", "10", "10", "10"}
	stackWith12s := func(n int) []string {
		stack := append([]string{}, prefix...)
		for range n {
			stack = append(stack, "12")
		}
		return stack
	}

	segments := []Segment{
		{Audio: sfx, Filters: stackWith12s(3), Duration: sfxDur},
		{Audio: sfx, Filters: stackWith12s(3), Duration: sfxDur},
		{Audio: sfx, Filters: stackWith12s(3), Duration: sfxDur},
		{Audio: sfx, Filters: stackWith12s(2), Duration: sfxDur},
		{Audio: sfx, Filters: stackWith12s(1), Duration: sfxDur},
		{Audio: sfx, Filters: stackWith12s(0), Duration: sfxDur},
	}

	client := ffmpeg.New(&ffmpeg.Config{TmpDir: t.TempDir()})
	ctx := context.Background()

	const runs = 3
	for _, impl := range []struct {
		name   string
		render renderer
	}{
		{"legacy", NewLegacy(client)},
		{"tree", New(client)},
	} {
		best := time.Duration(0)
		for i := range runs {
			start := time.Now()
			audio, _, err := impl.render.Render(ctx, segments, 500*time.Millisecond, false)
			if err != nil {
				t.Fatal(err)
			}
			elapsed := time.Since(start)
			if i == 0 || elapsed < best {
				best = elapsed
			}
			if i == 0 {
				probe, err := client.Ffprobe(ctx, audio)
				if err != nil {
					t.Fatal(err)
				}
				t.Logf("%s output duration: %v, mean volume: %.1f dB", impl.name, probe.Duration, meanVolume(t, audio))
			}
			t.Logf("%s run %d: %v", impl.name, i+1, elapsed)
		}
		t.Logf("%s best: %v", impl.name, best)
	}
}
