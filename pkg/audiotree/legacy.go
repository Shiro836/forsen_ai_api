package audiotree

import (
	"context"
	"fmt"
	"time"

	"app/pkg/ffmpeg"

	"golang.org/x/sync/errgroup"
)

// LegacyRenderer is the original per-segment pipeline behind the same
// signature as Renderer: every segment is filtered on its own (one ffmpeg
// pass per filter), then everything is concatenated. Filters spanning
// several segments restart at each segment boundary — the behavior the tree
// renderer exists to fix. Kept so the implementations stay interchangeable.
type LegacyRenderer struct {
	ffmpeg *ffmpeg.Client
}

func NewLegacy(ffmpegClient *ffmpeg.Client) *LegacyRenderer {
	return &LegacyRenderer{
		ffmpeg: ffmpegClient,
	}
}

func (r *LegacyRenderer) Render(ctx context.Context, segments []Segment, padding time.Duration, disableLimiter bool) ([]byte, []Placement, error) {
	if len(segments) == 0 {
		return nil, nil, fmt.Errorf("no segments to render")
	}

	audios := make([][]byte, len(segments))
	durations := make([]time.Duration, len(segments))

	eg, egCtx := errgroup.WithContext(ctx)
	for i, seg := range segments {
		eg.Go(func() error {
			audios[i], durations[i] = seg.Audio, seg.Duration
			if len(seg.Filters) == 0 {
				return nil
			}

			processed, err := r.ffmpeg.ApplyStringFilters(egCtx, seg.Audio, seg.Filters, disableLimiter)
			if err != nil {
				return fmt.Errorf("apply filters to segment %d: %w", i, err)
			}

			probe, err := r.ffmpeg.Ffprobe(egCtx, processed)
			if err != nil {
				return fmt.Errorf("probe filtered segment %d: %w", i, err)
			}

			audios[i], durations[i] = processed, probe.Duration
			return nil
		})
	}
	if err := eg.Wait(); err != nil {
		return nil, nil, err
	}

	combined, err := r.ffmpeg.ConcatenateAudio(ctx, padding, audios...)
	if err != nil {
		return nil, nil, fmt.Errorf("concatenate segments: %w", err)
	}

	placements := make([]Placement, len(segments))
	offset := time.Duration(0)
	for i, dur := range durations {
		if i > 0 {
			offset += padding
		}
		placements[i] = Placement{Start: offset, End: offset + dur}
		offset += dur
	}

	return combined, placements, nil
}
