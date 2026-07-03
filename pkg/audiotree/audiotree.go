// Package audiotree renders a sequence of audio segments carrying filter
// stacks into a single track. Filters pushed over several consecutive
// segments are applied once to their whole concatenated span, so background
// loops play continuously and sweep filters move across the full span instead
// of restarting at every segment.
//
// Each segment carries a snapshot of the filter stack that was active when it
// was produced. Because the stacks come from push/pop parsing they are well
// nested, which lets the snapshots be folded back into a tree: every filter
// instance becomes a node covering the contiguous run of segments it was
// active for. Sibling subtrees render in parallel, and each node is a single
// ffmpeg invocation (concat + the node's whole filter chain in one graph).
package audiotree

import (
	"context"
	"fmt"
	"os"
	"time"

	"app/pkg/ffmpeg"

	"golang.org/x/sync/errgroup"
)

type Segment struct {
	Audio []byte
	// Filters is the active filter stack for this segment, outermost first,
	// as numeric strings matching ffmpeg.FilterType values.
	Filters  []string
	Duration time.Duration
}

// Placement is where a segment ended up on the rendered track's timeline,
// accounting for concat padding and duration-changing filters.
type Placement struct {
	Start time.Duration
	End   time.Duration
}

type Renderer struct {
	ffmpeg *ffmpeg.Client
}

func New(ffmpegClient *ffmpeg.Client) *Renderer {
	return &Renderer{
		ffmpeg: ffmpegClient,
	}
}

// Render produces a single WAV track from the segments, with padding of
// silence between adjacent segments. Placements are indexed like segments.
func (r *Renderer) Render(ctx context.Context, segments []Segment, padding time.Duration, disableLimiter bool) ([]byte, []Placement, error) {
	if len(segments) == 0 {
		return nil, nil, fmt.Errorf("no segments to render")
	}

	stacks := make([][]ffmpeg.FilterType, len(segments))
	for i, seg := range segments {
		stack, err := parseStack(seg.Filters)
		if err != nil {
			return nil, nil, fmt.Errorf("segment %d: %w", i, err)
		}
		stacks[i] = stack
	}

	workDir, err := os.MkdirTemp(r.ffmpeg.TmpDir(), "audiotree_")
	if err != nil {
		return nil, nil, fmt.Errorf("create work dir: %w", err)
	}
	defer os.RemoveAll(workDir)

	run := &render{
		renderer:       r,
		dir:            workDir,
		padding:        padding,
		disableLimiter: disableLimiter,
	}

	leaves := make([]*rendered, len(segments))
	for i, seg := range segments {
		path, err := run.writeFile(seg.Audio)
		if err != nil {
			return nil, nil, fmt.Errorf("write segment %d: %w", i, err)
		}
		leaves[i] = &rendered{
			path: path,
			dur:  seg.Duration,
			placements: []leafPlacement{
				{segment: i, start: 0, end: seg.Duration},
			},
		}
	}

	root, err := run.renderNode(ctx, buildTree(stacks), leaves, true)
	if err != nil {
		return nil, nil, err
	}

	audio, err := os.ReadFile(root.path)
	if err != nil {
		return nil, nil, fmt.Errorf("read rendered output: %w", err)
	}

	placements := make([]Placement, len(segments))
	for _, lp := range root.placements {
		placements[lp.segment] = Placement{Start: lp.start, End: lp.end}
	}

	return audio, placements, nil
}

type leafPlacement struct {
	segment    int
	start, end time.Duration
}

type rendered struct {
	path       string
	dur        time.Duration
	placements []leafPlacement
}

type render struct {
	renderer       *Renderer
	dir            string
	padding        time.Duration
	disableLimiter bool
}

func (rn *render) renderNode(ctx context.Context, n *node, leaves []*rendered, final bool) (*rendered, error) {
	if n.leaf >= 0 {
		return leaves[n.leaf], nil
	}

	children := make([]*rendered, len(n.children))

	eg, egCtx := errgroup.WithContext(ctx)
	for i, child := range n.children {
		eg.Go(func() error {
			res, err := rn.renderNode(egCtx, child, leaves, false)
			if err != nil {
				return err
			}
			children[i] = res
			return nil
		})
	}
	if err := eg.Wait(); err != nil {
		return nil, err
	}

	if len(n.filters) == 0 && len(children) == 1 {
		return children[0], nil
	}

	return rn.run(ctx, children, n.filters, final)
}
