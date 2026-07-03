package audiotree

import (
	"context"
	"os"
	"testing"
	"time"

	"app/pkg/ffmpeg"
)

func TestConductorMessagePerformance(t *testing.T) {
	if testing.Short() {
		t.Skip("comparison benchmark")
	}

	clip, err := os.ReadFile("../ffmpeg/okayeg_ref.mp3")
	if err != nil {
		t.Fatal(err)
	}

	client := ffmpeg.New(&ffmpeg.Config{TmpDir: t.TempDir()})
	ctx := context.Background()

	stack := []string{"2", "4", "4", "4", "21", "21", "21", "24", "25", "25", "25"}
	segments := []Segment{{Audio: clip, Filters: stack, Duration: 10361 * time.Millisecond}}

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
			if _, _, err := impl.render.Render(ctx, segments, 500*time.Millisecond, false); err != nil {
				t.Fatal(err)
			}
			elapsed := time.Since(start)
			if i == 0 || elapsed < best {
				best = elapsed
			}
			t.Logf("%s run %d: %v", impl.name, i+1, elapsed)
		}
		t.Logf("%s best: %v", impl.name, best)
	}
}
