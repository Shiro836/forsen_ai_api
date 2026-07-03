package audiotree

import (
	"context"
	"fmt"
	"testing"
	"time"

	"app/pkg/ffmpeg"
)

func TestSpeedVsLegacy(t *testing.T) {
	if testing.Short() {
		t.Skip("comparison benchmark")
	}

	client := ffmpeg.New(&ffmpeg.Config{TmpDir: t.TempDir()})
	ctx := context.Background()
	clip := sine(t, 3*time.Second)

	keyboard := fmt.Sprintf("%d", ffmpeg.FilterBackgroundKeyboard)
	muffled := fmt.Sprintf("%d", ffmpeg.FilterMuffled)
	quiet := fmt.Sprintf("%d", ffmpeg.FilterQuiet)
	stacks := [][]string{
		{keyboard},
		{keyboard, muffled},
		{keyboard, muffled, quiet},
		{keyboard},
	}

	segments := make([]Segment, len(stacks))
	for i, stack := range stacks {
		segments[i] = Segment{Audio: clip, Filters: stack, Duration: 3 * time.Second}
	}


	legacyStart := time.Now()
	if _, _, err := NewLegacy(client).Render(ctx, segments, 500*time.Millisecond, false); err != nil {
		t.Fatal(err)
	}
	legacy := time.Since(legacyStart)

	treeStart := time.Now()
	if _, _, err := New(client).Render(ctx, segments, 500*time.Millisecond, false); err != nil {
		t.Fatal(err)
	}
	tree := time.Since(treeStart)

	t.Logf("legacy: %v, tree: %v", legacy, tree)
}
