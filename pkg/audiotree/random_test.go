package audiotree

import (
	"context"
	"fmt"
	"math/rand"
	"slices"
	"testing"
	"time"

	"app/pkg/ffmpeg"
)

// effectiveOrders returns, per segment, the filters applied to it in
// application order: a node's filters run when its subtree is rendered,
// before any ancestor's.
func effectiveOrders(root *node, n int) [][]ffmpeg.FilterType {
	out := make([][]ffmpeg.FilterType, n)
	var walk func(nd *node, above []ffmpeg.FilterType)
	walk = func(nd *node, above []ffmpeg.FilterType) {
		if nd.leaf >= 0 {
			out[nd.leaf] = above
			return
		}
		cur := append(slices.Clone(nd.filters), above...)
		for _, child := range nd.children {
			walk(child, cur)
		}
	}
	walk(root, nil)
	return out
}

// TestRandomScriptsProcessingOrder generates random push/pop scripts and
// checks every segment's filter application order against the contract:
// filters covering the same span run in push order (the legacy per-segment
// order), spans nested inside run before the spans containing them.
func TestRandomScriptsProcessingOrder(t *testing.T) {
	type inst struct {
		value      ffmpeg.FilterType
		start, end int
	}

	r := rand.New(rand.NewSource(1))

	for iter := range 5000 {
		n := 1 + r.Intn(6)

		pool := r.Perm(int(ffmpeg.FilterLast) - 1)
		poolIdx := 0

		var active []*inst
		snaps := make([][]*inst, n)
		for seg := range n {
			for len(active) > 0 && r.Intn(3) == 0 {
				active[len(active)-1].end = seg
				active = active[:len(active)-1]
			}
			for poolIdx < len(pool) && len(active) < 8 && r.Intn(3) == 0 {
				active = append(active, &inst{value: ffmpeg.FilterType(pool[poolIdx] + 1), start: seg})
				poolIdx++
			}
			snaps[seg] = slices.Clone(active)
		}
		for _, in := range active {
			in.end = n
		}

		stacks := make([][]ffmpeg.FilterType, n)
		for seg, snap := range snaps {
			for _, in := range snap {
				stacks[seg] = append(stacks[seg], in.value)
			}
		}

		got := effectiveOrders(buildTree(stacks), n)

		for seg, snap := range snaps {
			var want []ffmpeg.FilterType
			for g := len(snap) - 1; g >= 0; {
				h := g
				for h > 0 && snap[h-1].start == snap[g].start && snap[h-1].end == snap[g].end {
					h--
				}
				for _, in := range snap[h : g+1] {
					want = append(want, in.value)
				}
				g = h - 1
			}

			if !slices.Equal(got[seg], want) {
				t.Fatalf("iter %d segment %d: applied %v, want %v (stacks %v)",
					iter, seg, got[seg], want, stacks)
			}
		}
	}
}

// TestRandomSameSpanStacksMatchLegacy renders random single-segment stacks
// through both engines. With one segment every stack is same-span, where the
// engines must process filters in the identical order — any order divergence
// shows up as a duration or loudness mismatch on non-commutative chains.
func TestRandomSameSpanStacksMatchLegacy(t *testing.T) {
	client := ffmpeg.New(&ffmpeg.Config{TmpDir: t.TempDir()})
	ctx := context.Background()
	clip := sine(t, 2*time.Second)

	r := rand.New(rand.NewSource(2))

	for iter := range 10 {
		stack := make([]string, 1+r.Intn(4))
		for i := range stack {
			stack[i] = fmt.Sprintf("%d", 1+r.Intn(int(ffmpeg.FilterLast)-1))
		}

		segments := []Segment{{Audio: clip, Filters: stack, Duration: 2 * time.Second}}

		legacyAudio, _, err := NewLegacy(client).Render(ctx, segments, 500*time.Millisecond, false)
		if err != nil {
			t.Fatalf("iter %d stack %v: legacy: %v", iter, stack, err)
		}
		treeAudio, _, err := New(client).Render(ctx, segments, 500*time.Millisecond, false)
		if err != nil {
			t.Fatalf("iter %d stack %v: tree: %v", iter, stack, err)
		}

		legacyProbe, err := client.Ffprobe(ctx, legacyAudio)
		if err != nil {
			t.Fatal(err)
		}
		treeProbe, err := client.Ffprobe(ctx, treeAudio)
		if err != nil {
			t.Fatal(err)
		}

		durDiff := (legacyProbe.Duration - treeProbe.Duration).Abs()
		volDiff := meanVolume(t, legacyAudio) - meanVolume(t, treeAudio)

		t.Logf("iter %d stack %v: duration %v vs %v, volume diff %.1f dB",
			iter, stack, legacyProbe.Duration, treeProbe.Duration, volDiff)

		if durDiff > 350*time.Millisecond {
			t.Errorf("iter %d stack %v: durations diverge: legacy %v, tree %v",
				iter, stack, legacyProbe.Duration, treeProbe.Duration)
		}
		if volDiff > 3 || volDiff < -3 {
			t.Errorf("iter %d stack %v: loudness diverges by %.1f dB", iter, stack, volDiff)
		}
	}
}

// TestRandomNestedSpansRenderStability throws random nested multi-segment
// filter scripts at the tree renderer and requires a successful render with
// sane, ordered placements — no graph-assembly failures anywhere in the
// filter space.
func TestRandomNestedSpansRenderStability(t *testing.T) {
	client := ffmpeg.New(&ffmpeg.Config{TmpDir: t.TempDir()})
	ctx := context.Background()
	clip := sine(t, time.Second)

	r := rand.New(rand.NewSource(3))

	for iter := range 8 {
		n := 2 + r.Intn(4)

		var active []string
		segments := make([]Segment, n)
		var stacks [][]string
		for seg := range n {
			for len(active) > 0 && r.Intn(3) == 0 {
				active = active[:len(active)-1]
			}
			for len(active) < 6 && r.Intn(2) == 0 {
				active = append(active, fmt.Sprintf("%d", 1+r.Intn(int(ffmpeg.FilterLast)-1)))
			}
			stack := slices.Clone(active)
			stacks = append(stacks, stack)
			segments[seg] = Segment{Audio: clip, Filters: stack, Duration: time.Second}
		}

		audio, placements, err := New(client).Render(ctx, segments, 500*time.Millisecond, false)
		if err != nil {
			t.Fatalf("iter %d stacks %v: %v", iter, stacks, err)
		}

		probe, err := client.Ffprobe(ctx, audio)
		if err != nil {
			t.Fatal(err)
		}
		if probe.Duration <= 0 {
			t.Errorf("iter %d stacks %v: empty output", iter, stacks)
		}

		for i := 1; i < n; i++ {
			if placements[i].Start < placements[i-1].Start {
				t.Errorf("iter %d stacks %v: placements out of order: %+v", iter, stacks, placements)
			}
		}
	}
}
