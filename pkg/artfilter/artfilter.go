// Package artfilter detects character-art regions — braille walls, box-drawing
// pictures, symbol spam — by Unicode range. Art is judged by character class,
// not meaning: an LLM cannot see the 2D shape in a token stream, so a
// deterministic pass has to decide what counts as art. Thresholds were measured
// on the real message corpus (no legitimate message trips them).
package artfilter

import (
	"app/pkg/textfilter"
)

const (
	// Real prose never contains braille; art always has dozens. Interleaving
	// filler between art characters defeats run-length rules (seen in the
	// wild), so braille is counted per message, not per run.
	brailleMin = 4

	shapeRunMin  = 3
	symbolRunMin = 6

	// Unmarked runes sandwiched between art regions this closely are
	// interleave filler; bridging them keeps one placeholder per wall.
	bridgeGap = 2

	// When art dominates the stretch from the first art rune to the last, the
	// unmarked remainder is row decoration ("HH" borders between braille
	// rows), not content — the whole stretch collapses to one span. Real
	// sentences between two separate arts push the density below this.
	hullDensity = 0.6
)

// Detection carries per-message decisions that must survive splitting the
// message into TTS chunks: a chunk of an interleaved braille wall may hold too
// few braille characters to trip the threshold on its own.
type Detection struct {
	BrailleArt bool
}

// Detect scans a whole message. Spans/Mask may then be applied to the message
// itself or to any chunk of it.
func Detect(text string) Detection {
	n := 0
	for _, r := range text {
		if isBraille(r) {
			n++
			if n >= brailleMin {
				return Detection{BrailleArt: true}
			}
		}
	}
	return Detection{}
}

// Spans returns the art regions of text as rune offsets.
func (d Detection) Spans(text string) []textfilter.Span {
	runes := []rune(text)
	marks := make([]bool, len(runes))

	if d.BrailleArt {
		for i, r := range runes {
			if isBraille(r) {
				marks[i] = true
			}
		}
	}

	markRuns(runes, marks, isShape, shapeRunMin)
	markRuns(runes, marks, isSymbol, symbolRunMin)

	spans := spansFromMarks(marks)

	if len(spans) > 1 {
		marked := 0
		for _, s := range spans {
			marked += s.End - s.Start
		}
		hull := textfilter.Span{Start: spans[0].Start, End: spans[len(spans)-1].End}
		if float64(marked) >= hullDensity*float64(hull.End-hull.Start) {
			spans = []textfilter.Span{extendOverBorders(runes, hull)}
		}
	}

	return spans
}

// extendOverBorders grows a dense wall's span over short runs glued directly to
// its edges — the outermost copies of the same row decoration the hull already
// swallowed between rows. Only called after the density test, so a word glued
// to a lone art run is never eaten.
func extendOverBorders(runes []rune, s textfilter.Span) textfilter.Span {
	const borderMax = 3

	i := s.Start
	for i > 0 && !isSpace(runes[i-1]) {
		i--
	}
	if s.Start-i <= borderMax {
		s.Start = i
	}

	j := s.End
	for j < len(runes) && !isSpace(runes[j]) {
		j++
	}
	if j-s.End <= borderMax {
		s.End = j
	}

	return s
}

func isSpace(r rune) bool {
	return r == ' ' || r == '\t' || r == '\n' || r == '\r'
}

// Mask replaces each art region with repl.
func (d Detection) Mask(text, repl string) string {
	return textfilter.Censor(text, d.Spans(text), repl)
}

func isBraille(r rune) bool {
	return r >= 0x2800 && r <= 0x28FF
}

// isShape covers the blocks that draw structure: box drawing, block elements,
// geometric shapes (and their extended plane-1 block).
func isShape(r rune) bool {
	return (r >= 0x2500 && r <= 0x25FF) || (r >= 0x1F780 && r <= 0x1F7FF)
}

// isSymbol covers decoration spam: arrows, misc technical, misc symbols,
// dingbats.
func isSymbol(r rune) bool {
	return (r >= 0x2190 && r <= 0x21FF) || (r >= 0x2300 && r <= 0x23FF) || (r >= 0x2600 && r <= 0x27BF)
}

// emojiPresentation reports whether the rune at i is followed by VS16, which
// turns symbols like a dingbat heart into a legitimate emoji.
func emojiPresentation(runes []rune, i int) bool {
	return i+1 < len(runes) && runes[i+1] == 0xFE0F
}

func markRuns(runes []rune, marks []bool, class func(rune) bool, min int) {
	i := 0
	for i < len(runes) {
		if !class(runes[i]) || emojiPresentation(runes, i) {
			i++
			continue
		}
		j := i
		for j < len(runes) && class(runes[j]) && !emojiPresentation(runes, j) {
			j++
		}
		if j-i >= min {
			for k := i; k < j; k++ {
				marks[k] = true
			}
		}
		i = j
	}
}

func spansFromMarks(marks []bool) []textfilter.Span {
	var out []textfilter.Span
	for i := 0; i < len(marks); i++ {
		if !marks[i] {
			continue
		}
		j := i + 1
		for j < len(marks) && marks[j] {
			j++
		}
		if len(out) > 0 && i-out[len(out)-1].End <= bridgeGap {
			out[len(out)-1].End = j
		} else {
			out = append(out, textfilter.Span{Start: i, End: j})
		}
		i = j
	}
	return out
}
