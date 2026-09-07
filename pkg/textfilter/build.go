package textfilter

import "strings"

// Builder derives one text from another — the spoken form of a message from
// its raw form — recording where every derived rune came from, so spans found
// over the derived text map back to the source. Source runes are consumed in
// order: each call covers the source from where the previous one stopped up
// to end.
type Builder struct {
	src []rune
	pos int
	out strings.Builder
	m   Mapping
}

func NewBuilder(src string) *Builder {
	return &Builder{src: []rune(src)}
}

// Copy carries the source through unchanged up to end.
func (b *Builder) Copy(end int) {
	for i := b.pos; i < end; i++ {
		b.m.origStart = append(b.m.origStart, i)
		b.m.origEnd = append(b.m.origEnd, i+1)
	}
	b.out.WriteString(string(b.src[b.pos:end]))
	b.pos = end
}

// Replace stands repl in for the source up to end. A span touching repl maps
// back onto the whole replaced range.
func (b *Builder) Replace(end int, repl string) {
	for range repl {
		b.m.origStart = append(b.m.origStart, b.pos)
		b.m.origEnd = append(b.m.origEnd, end)
	}
	b.out.WriteString(repl)
	b.pos = end
}

// Insert adds text with no source counterpart: a span over it alone maps to
// nothing, a span reaching past it keeps only its source part.
func (b *Builder) Insert(text string) {
	for range text {
		b.m.origStart = append(b.m.origStart, b.pos)
		b.m.origEnd = append(b.m.origEnd, b.pos)
	}
	b.out.WriteString(text)
}

// Skip leaves the source out up to end; a mapped-back span never covers it.
func (b *Builder) Skip(end int) {
	if end > b.pos {
		b.m.skipped = append(b.m.skipped, Span{Start: b.pos, End: end})
	}
	b.pos = end
}

// Len is the number of runes derived so far.
func (b *Builder) Len() int {
	return len(b.m.origStart)
}

// Build copies the rest of the source through and returns the derived text
// with its mapping.
func (b *Builder) Build() (string, *Mapping) {
	b.Copy(len(b.src))
	return b.out.String(), &b.m
}

// Window keeps the parts of spans inside [start, end), re-based to start, so
// that slice of the text can be censored or highlighted on its own.
func Window(spans []Span, start, end int) []Span {
	var out []Span
	for _, s := range spans {
		lo, hi := max(s.Start, start), min(s.End, end)
		if lo >= hi {
			continue
		}
		out = append(out, Span{Start: lo - start, End: hi - start})
	}
	return out
}
