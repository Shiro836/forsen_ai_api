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

// Mapping translates rune offsets over a Collapse result back to the original
// text. A nil Mapping is the identity.
type Mapping struct {
	origStart []int
	origEnd   []int
}

// Collapse replaces each span with repl, like Censor, and returns a Mapping so
// spans found over the collapsed text can be translated back to the original.
// spans must be ordered and non-overlapping (as returned by Merge).
func Collapse(text string, spans []Span, repl string) (string, *Mapping) {
	if len(spans) == 0 {
		return text, nil
	}

	r := []rune(text)
	replRunes := []rune(repl)

	var b strings.Builder
	m := &Mapping{}

	appendLiteral := func(from, to int) {
		for i := from; i < to; i++ {
			m.origStart = append(m.origStart, i)
			m.origEnd = append(m.origEnd, i+1)
		}
		b.WriteString(string(r[from:to]))
	}

	prev := 0
	for _, s := range spans {
		appendLiteral(prev, s.Start)
		for range replRunes {
			m.origStart = append(m.origStart, s.Start)
			m.origEnd = append(m.origEnd, s.End)
		}
		b.WriteString(repl)
		prev = s.End
	}
	appendLiteral(prev, len(r))

	return b.String(), m
}

// MapBack translates spans over the collapsed text to spans over the original.
// A span touching a replacement expands to cover the full replaced range.
func (m *Mapping) MapBack(spans []Span) []Span {
	if m == nil {
		return spans
	}

	var out []Span
	for _, s := range spans {
		if s.Start < 0 {
			s.Start = 0
		}
		if s.End > len(m.origEnd) {
			s.End = len(m.origEnd)
		}
		if s.Start >= s.End {
			continue
		}
		out = append(out, Span{Start: m.origStart[s.Start], End: m.origEnd[s.End-1]})
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
