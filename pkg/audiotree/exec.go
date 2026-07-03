package audiotree

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path"
	"strings"
	"time"

	"app/pkg/ffmpeg"

	"github.com/google/uuid"
)

const normalizeChain = "aformat=sample_rates=44100:sample_fmts=fltp:channel_layouts=stereo"

// run concatenates the children with silence padding and applies the filter
// list on top, all in one ffmpeg invocation. Filters are applied in slice
// order (innermost first). The whole graph works in floating point, so
// intermediate stages can't clip; only the final node limits, right before
// the output is quantized.
func (rn *render) run(ctx context.Context, children []*rendered, filters []ffmpeg.FilterType, final bool) (*rendered, error) {
	args := []string{"-nostats", "-loglevel", "error"}
	for _, child := range children {
		args = append(args, "-i", child.path)
	}

	inputIdx := len(children)
	var graph []string

	for i := range children {
		graph = append(graph, fmt.Sprintf("[%d:a]%s[c%d]", i, normalizeChain, i))
	}

	cur := "c0"
	if len(children) > 1 {
		var concat strings.Builder
		for i := range children {
			if i > 0 && rn.padding > 0 {
				graph = append(graph, fmt.Sprintf(
					"anullsrc=channel_layout=stereo:sample_rate=44100,atrim=duration=%.3f,%s[p%d]",
					rn.padding.Seconds(), normalizeChain, i))
				fmt.Fprintf(&concat, "[p%d]", i)
			}
			fmt.Fprintf(&concat, "[c%d]", i)
		}

		total := len(children)
		if rn.padding > 0 {
			total += len(children) - 1
		}
		graph = append(graph, fmt.Sprintf("%sconcat=n=%d:v=0:a=1[cat]", concat.String(), total))
		cur = "cat"
	}

	inDur := time.Duration(0)
	for i, child := range children {
		if i > 0 {
			inDur += rn.padding
		}
		inDur += child.dur
	}

	// placements map to the output timeline as t' = t*mul + shift
	mul := 1.0
	shift := time.Duration(0)
	curDur := inDur

	for k, filter := range filters {
		next := fmt.Sprintf("f%d", k)

		switch kindOf(filter) {
		case stageChain:
			graph = append(graph, fmt.Sprintf("[%s]%s[%s]", cur, chainFor(filter, curDur), next))
			m := durationMultiplier(filter)
			mul *= m
			shift = time.Duration(float64(shift) * m)
			curDur = time.Duration(float64(curDur) * m)

		case stageBGLoop:
			bgPath, err := rn.writeFile(filter.BackgroundAudio())
			if err != nil {
				return nil, err
			}
			args = append(args, "-stream_loop", "-1", "-i", bgPath)
			graph = append(graph,
				fmt.Sprintf("[%d:a]%s[bg%d]", inputIdx, normalizeChain, k),
				fmt.Sprintf("[%s][bg%d]amix=inputs=2:duration=first:dropout_transition=0[%s]", cur, k, next))
			inputIdx++

		case stageIRMix:
			irPath, err := rn.writeFile(filter.BackgroundAudio())
			if err != nil {
				return nil, err
			}
			args = append(args, "-i", irPath)
			graph = append(graph,
				fmt.Sprintf("[%s]asplit[dry%d][wet%d]", cur, k, k),
				fmt.Sprintf("[wet%d][%d:a]afir=dry=10:wet=10[rev%d]", k, inputIdx, k),
				fmt.Sprintf("[dry%d][rev%d]amix=inputs=2:weights=10 1[%s]", k, k, next))
			inputIdx++

		case stageGhost:
			irPath, err := rn.writeFile(filter.BackgroundAudio())
			if err != nil {
				return nil, err
			}
			args = append(args, "-i", irPath)
			graph = append(graph,
				fmt.Sprintf("[%s]asplit[gdry%d][gwet%d]", cur, k, k),
				fmt.Sprintf("[gwet%d]adelay=1000|1000,areverse[grev%d]", k, k),
				fmt.Sprintf("[grev%d][%d:a]afir=dry=10:wet=10[gfir%d]", k, inputIdx, k),
				fmt.Sprintf("[gfir%d]areverse[gtail%d]", k, k),
				fmt.Sprintf("[gdry%d]adelay=1000|1000[gmain%d]", k, k),
				fmt.Sprintf("[gmain%d][gtail%d]amix=inputs=2:weights=10 5[%s]", k, k, next))
			inputIdx++
			shift += time.Second
			curDur += time.Second
		}

		cur = next
	}

	if final && !rn.disableLimiter {
		graph = append(graph, fmt.Sprintf("[%s]alimiter=limit=0.9:attack=5:release=50[lim]", cur))
		cur = "lim"
	}

	outPath := path.Join(rn.dir, uuid.NewString()+".wav")
	args = append(args,
		"-filter_complex", strings.Join(graph, ";"),
		"-map", "["+cur+"]",
		"-f", "wav",
		"-y",
		outPath,
	)

	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	dbgStart := time.Now()
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg node render: %w\nffmpeg output:\n%s", err, strings.TrimSpace(stderr.String()))
	}
	if os.Getenv("AUDIOTREE_DEBUG") != "" {
		fmt.Printf("NODE %v filters=%v inputs=%d args=%q\n", time.Since(dbgStart), filters, len(children), args)
	}

	probe, err := rn.renderer.ffmpeg.FfprobePath(ctx, outPath)
	if err != nil {
		return nil, fmt.Errorf("probe node output: %w", err)
	}

	// Filters like echo and reverb lengthen audio in ways the multipliers
	// above don't predict; fold a measured correction into the transform,
	// ignoring small tails that would skew timings for no audible reason.
	if curDur > 0 {
		residual := float64(probe.Duration) / float64(curDur)
		if math.Abs(residual-1) > 0.05 {
			mul *= residual
			shift = time.Duration(float64(shift) * residual)
		}
	}

	out := &rendered{path: outPath, dur: probe.Duration}

	offset := time.Duration(0)
	for i, child := range children {
		if i > 0 {
			offset += rn.padding
		}
		for _, lp := range child.placements {
			out.placements = append(out.placements, leafPlacement{
				segment: lp.segment,
				start:   time.Duration(float64(lp.start+offset)*mul) + shift,
				end:     time.Duration(float64(lp.end+offset)*mul) + shift,
			})
		}
		offset += child.dur
	}

	return out, nil
}

func (rn *render) writeFile(data []byte) (string, error) {
	filePath := path.Join(rn.dir, uuid.NewString())
	if err := os.WriteFile(filePath, data, 0644); err != nil {
		return "", fmt.Errorf("write temp file: %w", err)
	}
	return filePath, nil
}
