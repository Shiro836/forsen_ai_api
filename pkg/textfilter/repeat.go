package textfilter

// Repetition spam defeats the LLM filter twice over: the model cannot echo
// hundreds of runes of the same unit verbatim, and a slur-sounding unit
// buried in its own repeats is judged as noise. Collapsing each run to a few
// copies gives the model a message it can read, and expanding any span that
// lands on the kept copies over the whole run keeps every repeat censored.
const (
	keptRepeats = 3
	minRepeats  = 4
	minUnit     = 2
	maxUnit     = 64
)

type repeatRun struct {
	start, unit, count int // rune offset, unit length in runes, repeats
}

// CollapseRepeats shortens every run of a unit repeated minRepeats or more
// times to keptRepeats copies. The returned function maps spans found over the
// collapsed text back to the original, widening a span that touches a
// collapsed run to cover the whole run.
func CollapseRepeats(text string) (string, func([]Span) []Span) {
	r := []rune(text)
	runs := findRepeats(r)
	if len(runs) == 0 {
		return text, func(s []Span) []Span { return s }
	}

	// origOf[i] is the original offset of collapsed rune i; a dropped run
	// tail has no collapsed runes, so a span ending on the kept copies is
	// what marks the run.
	var out []rune
	var origOf []int
	var kept []Span // kept region of each run, in collapsed offsets
	var full []Span // whole run, in original offsets
	prev := 0
	for _, run := range runs {
		for i := prev; i < run.start; i++ {
			out = append(out, r[i])
			origOf = append(origOf, i)
		}
		keepEnd := run.start + keptRepeats*run.unit
		kept = append(kept, Span{Start: len(out), End: len(out) + keptRepeats*run.unit})
		full = append(full, Span{Start: run.start, End: run.start + run.count*run.unit})
		for i := run.start; i < keepEnd; i++ {
			out = append(out, r[i])
			origOf = append(origOf, i)
		}
		prev = run.start + run.count*run.unit
	}
	for i := prev; i < len(r); i++ {
		out = append(out, r[i])
		origOf = append(origOf, i)
	}

	expand := func(spans []Span) []Span {
		var mapped []Span
		for _, s := range spans {
			if s.Start < 0 {
				s.Start = 0
			}
			if s.End > len(origOf) {
				s.End = len(origOf)
			}
			if s.Start >= s.End {
				continue
			}
			m := Span{Start: origOf[s.Start], End: origOf[s.End-1] + 1}
			for i, k := range kept {
				if s.Start < k.End && k.Start < s.End {
					m.Start = min(m.Start, full[i].Start)
					m.End = max(m.End, full[i].End)
				}
			}
			mapped = append(mapped, m)
		}
		return Merge(mapped)
	}
	return string(out), expand
}

// findRepeats scans left to right for the shortest unit that repeats at
// least minRepeats times from a position; runs never overlap.
func findRepeats(r []rune) []repeatRun {
	var runs []repeatRun
	i := 0
	for i < len(r) {
		found := false
		for unit := minUnit; unit <= maxUnit && i+unit*minRepeats <= len(r); unit++ {
			count := 1
			for j := i + unit; j+unit <= len(r) && equalRunes(r[i:i+unit], r[j:j+unit]); j += unit {
				count++
			}
			if count >= minRepeats {
				runs = append(runs, repeatRun{start: i, unit: unit, count: count})
				i += unit * count
				found = true
				break
			}
		}
		if !found {
			i++
		}
	}
	return runs
}

func equalRunes(a, b []rune) bool {
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
