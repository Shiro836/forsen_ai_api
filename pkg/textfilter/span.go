// Package textfilter holds the shared vocabulary for content filtering: a Span
// type (rune offsets), span merging, and censoring. Concrete filters (regex,
// LLM) produce spans over the original text; callers merge them and censor or
// highlight from the one set.
package textfilter

import (
	"sort"
	"strings"
)

// Span is a half-open range [Start, End) of rune offsets into the text.
type Span struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

// Merge combines span sets into one ordered set with overlaps coalesced.
func Merge(sets ...[]Span) []Span {
	var all []Span
	for _, s := range sets {
		all = append(all, s...)
	}
	if len(all) == 0 {
		return nil
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Start < all[j].Start })

	out := []Span{all[0]}
	for _, s := range all[1:] {
		last := &out[len(out)-1]
		if s.Start <= last.End {
			if s.End > last.End {
				last.End = s.End
			}
			continue
		}
		out = append(out, s)
	}
	return out
}

// Censor replaces each span with repl. spans must be ordered and non-overlapping
// (as returned by Merge).
func Censor(text string, spans []Span, repl string) string {
	if len(spans) == 0 {
		return text
	}
	r := []rune(text)

	var b strings.Builder
	prev := 0
	for _, s := range spans {
		b.WriteString(string(r[prev:s.Start]))
		b.WriteString(repl)
		prev = s.End
	}
	b.WriteString(string(r[prev:]))
	return b.String()
}
