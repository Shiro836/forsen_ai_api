package emoteservice

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
)

// FlashMeasure is one animation's photosensitive-flash score, in flashes per
// second, taken as the worse of a black and a white background. Hz is the
// verdict number; HzMax is the same animation under the literal WCAG
// per-direction area gate, stored so the rule can be revisited from the numbers
// instead of by rescoring every emote.
type FlashMeasure struct {
	Hz       float64 `json:"flash_hz"`
	HzMax    float64 `json:"flash_hz_max"`
	RedHz    float64 `json:"red_flash_hz"`
	Duration float64 `json:"duration"`
}

// Flashing is the hazard verdict: WCAG SC 2.3.1's "more than three flashes in
// any one second period", general or red.
func (m FlashMeasure) Flashing() bool {
	return m.Hz > flashHazardHz || m.RedHz > flashHazardHz
}

const (
	// flashBlock is the analysis cell size in pixels, and it is load-bearing: WCAG
	// exempts fine balanced patterns, and per-pixel the TV-static emote scores
	// 19 Hz where block 8 scores 0 with every measured strober untouched. One cell
	// is 1/16 of an emote's width, which is about the 0.1 degree of visual field
	// the exemption is written for.
	flashBlock = 8
	// flashFT is WCAG's general flash threshold and flashDT its darker-image
	// condition: a pair of opposing luminance changes of 10% or more where the
	// darker of the two states is below 0.80.
	flashFT = 0.10
	flashDT = 0.80
	// flashArea is the fraction of the field a transition must cover. WCAG's
	// 341x256 window is larger than a whole emote, which would make every emote
	// pass by construction, so the field is the emote's own opaque footprint.
	flashArea = 0.25
	// redFT is the legacy WCAG 2.0/2.1 red transition threshold on the saturated
	// red signal. The 2.2 restatement in CIE 1976 UCS needs a colour-space
	// conversion no real implementation ships.
	redFT         = 20.0
	flashHazardHz = 3.0
	// A frame shorter than this is a browser anti-flicker case, not a real
	// duration: an emote declaring 0 ms on all 174 of its frames exists in the
	// corpus, and unclamped its whole animation collapses into one instant and
	// reads as a hazard.
	flashMinFrameMS = 11
	flashClampMS    = 100
	flashMaxFrames  = 2000
	flashMaxRepeats = 60
	// The measure needs a few seconds of signal, and an emote loops forever.
	flashTargetSeconds = 3.0
)

// FrameSource yields an animation's coalesced frames one at a time. The measure
// never holds more than one decoded frame: the longest emotes in the corpus cost
// 466 MB of RSS to coalesce and must not cost that again to score.
type FrameSource interface {
	Len() int
	Frame(i int) (image.Image, error)
}

// MeasureFlash scores an animation for photosensitive flashing. durationsMS is
// the per-frame ANMF duration list; a length that disagrees with the frame count
// is discarded in favour of a uniform 100 ms, since a wrong timeline is worse
// than a nominal one. Fewer than two frames cannot flash and score zero.
func MeasureFlash(frames FrameSource, durationsMS []int) (FlashMeasure, error) {
	n := frames.Len()
	if n < 2 {
		return FlashMeasure{}, nil
	}

	starts, total := frameStarts(clampDurations(durationsMS, n))
	black, white, field, err := reduceFrames(frames)
	if err != nil {
		return FlashMeasure{}, err
	}
	order, times := repeatSequence(starts, total, n)

	m := FlashMeasure{Duration: total}
	for _, g := range []backgroundGrid{black, white} {
		up, down := transitions(g.lum, field, order, flashFT, flashDT)
		m.Hz = max(m.Hz, rate(countFlashes(up, down, times, true), total))
		m.HzMax = max(m.HzMax, rate(countFlashes(up, down, times, false), total))

		// Red flash keeps the max-area gate: it exists to catch a saturated red
		// that swings no luminance, and the coherence requirement only costs it
		// recall — measured over the top corpus it has no false positives to lose.
		rup, rdown := transitions(g.red, field, order, redFT, math.Inf(1))
		m.RedHz = max(m.RedHz, rate(countFlashes(rup, rdown, times, false), total))
	}
	return m, nil
}

// rate drops the first repetition, whose flashes are the state machine warming
// up, and takes the busiest sliding one-second window of what is left.
func rate(flashes []float64, total float64) float64 {
	best := 0
	for _, t0 := range flashes {
		if t0 < total {
			continue
		}
		n := 0
		for _, t := range flashes {
			if t >= t0 && t < t0+1 {
				n++
			}
		}
		best = max(best, n)
	}
	return float64(best)
}

func clampDurations(durs []int, frames int) []int {
	out := make([]int, frames)
	for i := range out {
		out[i] = flashClampMS
		if len(durs) == frames && durs[i] >= flashMinFrameMS {
			out[i] = durs[i]
		}
	}
	return out
}

func frameStarts(durs []int) (starts []float64, total float64) {
	starts = make([]float64, len(durs))
	for i, d := range durs {
		starts[i] = total
		total += float64(d) / 1000
	}
	return starts, total
}

// repeatSequence walks the loop several times over, which is what gives a
// sub-second emote a meaningful rate and makes the seam between its last and
// first frame the real transition it is on screen.
func repeatSequence(starts []float64, total float64, frames int) (order []int, times []float64) {
	reps := max(2, min(flashMaxRepeats, int(math.Ceil(flashTargetSeconds/total))+1))
	reps = max(2, min(reps, max(2, flashMaxFrames/frames)))

	order = make([]int, 0, reps*frames)
	times = make([]float64, 0, reps*frames)
	for r := range reps {
		for i, s := range starts {
			order = append(order, i)
			times = append(times, s+float64(r)*total)
		}
	}
	return order, times
}

// backgroundGrid is one background's block-reduced view of an animation: mean
// relative luminance and mean red-flash signal per cell per frame.
type backgroundGrid struct {
	lum [][]float64
	red [][]float64
}

// reduceFrames composites every frame over black and over white in linear light
// and averages both views down to the 8x8-pixel analysis grid. The field it
// returns is the emote's footprint: cells the emote paints at any point in the
// animation. Emotes are transparent and nothing controls what sits behind them
// on the overlay, so the two extremes bound the luminance swing a viewer can
// actually see — near the hazard line the backgrounds disagree in both
// directions, so neither can be dropped.
func reduceFrames(frames FrameSource) (black, white backgroundGrid, field []bool, err error) {
	n := frames.Len()
	var cols, rows, cells int
	var alphaMax []float64

	for i := range n {
		img, ferr := frames.Frame(i)
		if ferr != nil {
			return black, white, nil, ferr
		}
		b := img.Bounds()
		w, h := b.Dx(), b.Dy()
		if i == 0 {
			cols, rows = ceilDiv(w, flashBlock), ceilDiv(h, flashBlock)
			cells = cols * rows
			alphaMax = make([]float64, cells)
			black = newBackgroundGrid(n, cells)
			white = newBackgroundGrid(n, cells)
		} else if ceilDiv(w, flashBlock) != cols || ceilDiv(h, flashBlock) != rows {
			return black, white, nil, fmt.Errorf("frame %d is %dx%d, expected the canvas of frame 0", i, w, h)
		}

		alpha := make([]float64, cells)
		lumB, lumW := black.lum[i], white.lum[i]
		redB, redW := black.red[i], white.red[i]

		// The padded loop repeats edge pixels into a partial cell, so every cell
		// averages the same number of samples whatever the canvas size.
		for py := range rows * flashBlock {
			y := b.Min.Y + min(py, h-1)
			row := (py / flashBlock) * cols
			for px := range cols * flashBlock {
				x := b.Min.X + min(px, w-1)
				c := row + px/flashBlock

				r8, g8, b8, a8 := straightAt(img, x, y)
				a := float64(a8) / 255
				lr, lg, lb := srgbToLinear[r8]*a, srgbToLinear[g8]*a, srgbToLinear[b8]*a
				lum := 0.2126*lr + 0.7152*lg + 0.0722*lb

				alpha[c] += a
				lumB[c] += lum
				// White is 1.0 in every channel and the luminance weights sum to 1,
				// so what a white background adds is exactly the transparency.
				lumW[c] += lum + 1 - a
				sb, sw := redSignals(r8, g8, b8, a8, lr, lg, lb)
				redB[c] += sb
				redW[c] += sw
			}
		}

		norm := float64(flashBlock * flashBlock)
		for c := range cells {
			lumB[c] /= norm
			lumW[c] /= norm
			redB[c] /= norm
			redW[c] /= norm
			alphaMax[c] = max(alphaMax[c], alpha[c]/norm)
		}
	}

	field = make([]bool, cells)
	painted := false
	for c, a := range alphaMax {
		field[c] = a >= 0.5
		painted = painted || field[c]
	}
	if !painted {
		for c := range field {
			field[c] = true
		}
	}
	return black, white, field, nil
}

func newBackgroundGrid(frames, cells int) backgroundGrid {
	g := backgroundGrid{lum: make([][]float64, frames), red: make([][]float64, frames)}
	for i := range frames {
		g.lum[i] = make([]float64, cells)
		g.red[i] = make([]float64, cells)
	}
	return g
}

func ceilDiv(a, b int) int { return (a + b - 1) / b }

// transitions is the per-cell opposing-change detector. It reports the fraction
// of the field that completed a rising and a falling transition at each step of
// the repeated sequence. Each change is measured against the cell's last extreme
// rather than its previous frame, which is what makes the measure frame-rate
// independent: a swing spread over three fast frames counts once, exactly as the
// same swing taken in one slow step does.
func transitions(values [][]float64, field []bool, order []int, ft, dt float64) (up, down []float64) {
	size := 0
	for _, in := range field {
		if in {
			size++
		}
	}
	size = max(size, 1)

	ext := make([]float64, len(field))
	copy(ext, values[order[0]])
	sign := make([]int8, len(field))

	up = make([]float64, len(order))
	down = make([]float64, len(order))
	for t := 1; t < len(order); t++ {
		cur := values[order[t]]
		u, d := 0, 0
		for c, in := range field {
			if !in {
				continue
			}
			diff := cur[c] - ext[c]
			if math.Abs(diff) < ft || min(cur[c], ext[c]) >= dt {
				continue
			}
			s := int8(1)
			if diff <= 0 {
				s = -1
			}
			if s != sign[c] {
				if s > 0 {
					u++
				} else {
					d++
				}
				sign[c] = s
			}
			// A same-direction step only extends the current excursion.
			ext[c] = cur[c]
		}
		up[t] = float64(u) / float64(size)
		down[t] = float64(d) / float64(size)
	}
	return up, down
}

// countFlashes area-gates the per-frame transitions and pairs opposing ones into
// flashes, returning their timestamps. A flash is a *pair* of opposing changes,
// so three flashes per second is a 3 Hz square wave and six transitions.
//
// The net gate is the shipped rule: a moving high-contrast sprite brightens one
// edge of the field while darkening the other, so only a swing the whole field
// takes together survives the subtraction. WCAG's own per-direction gate,
// re-pointed at a sprite's own footprint, flags a Pepe blinking.
func countFlashes(up, down, times []float64, net bool) []float64 {
	var out []float64
	last := int8(0)
	half := 0
	for t := range up {
		gate := max(up[t], down[t])
		if net {
			gate = math.Abs(up[t] - down[t])
		}
		if gate <= flashArea {
			continue
		}
		s := int8(1)
		if up[t] < down[t] {
			s = -1
		}
		if s == last {
			continue
		}
		last = s
		if half++; half%2 == 0 {
			out = append(out, times[t])
		}
	}
	return out
}

// srgbToLinear is WCAG's relative-luminance linearisation of an 8-bit channel.
var srgbToLinear = func() [256]float64 {
	var lut [256]float64
	for i := range lut {
		c := float64(i) / 255
		if c <= 0.03928 {
			lut[i] = c / 12.92
		} else {
			lut[i] = math.Pow((c+0.055)/1.055, 2.4)
		}
	}
	return lut
}()

func linearToSRGB(c float64) float64 {
	c = min(max(c, 0), 1)
	if c <= 0.0031308 {
		return c * 12.92
	}
	return 1.055*math.Pow(c, 1/2.4) - 0.055
}

// redSignals is the legacy WCAG 2.0/2.1 saturated-red measure of one pixel over
// both backgrounds. Fully opaque and fully transparent pixels are nearly all of
// an emote and take the cheap path; the sRGB re-encode the formula is defined
// over is the only costly arithmetic in the measure.
func redSignals(r, g, b, a uint8, lr, lg, lb float64) (black, white float64) {
	switch a {
	case 255:
		s := redFromSRGB(float64(r)/255, float64(g)/255, float64(b)/255)
		return s, s
	case 0:
		// Transparent composites to the background itself, and neither black nor
		// white is a saturated red.
		return 0, 0
	}
	k := 1 - float64(a)/255
	return redFromSRGB(linearToSRGB(lr), linearToSRGB(lg), linearToSRGB(lb)),
		redFromSRGB(linearToSRGB(lr+k), linearToSRGB(lg+k), linearToSRGB(lb+k))
}

func redFromSRGB(r, g, b float64) float64 {
	total := r + g + b
	if total <= 1e-6 || r/total < 0.8 {
		return 0
	}
	return max(0, (r-g-b)*320)
}

// straightAt reads one pixel with straight alpha. Compositing in linear light
// needs the un-premultiplied colour, and image.Image.At premultiplies.
func straightAt(img image.Image, x, y int) (r, g, b, a uint8) {
	if nrgba, ok := img.(*image.NRGBA); ok {
		i := nrgba.PixOffset(x, y)
		return nrgba.Pix[i], nrgba.Pix[i+1], nrgba.Pix[i+2], nrgba.Pix[i+3]
	}
	c := color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA)
	return c.R, c.G, c.B, c.A
}

// pngFrames scores the coalesced frames straight off disk, which is where the
// grid builder has already written them.
type pngFrames []string

func (f pngFrames) Len() int { return len(f) }

func (f pngFrames) Frame(i int) (image.Image, error) {
	file, err := os.Open(f[i])
	if err != nil {
		return nil, fmt.Errorf("open frame: %w", err)
	}
	defer file.Close()

	img, err := png.Decode(file)
	if err != nil {
		return nil, fmt.Errorf("decode frame %s: %w", f[i], err)
	}
	return img, nil
}
