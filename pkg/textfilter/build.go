package textfilter

import "strings"

// Builder derives one text from another (the spoken form of a message from its
// raw form) while recording where each derived rune came from, so spans over
// the derived text map back to the source. Calls consume the source in order.
type Builder struct {
	src []rune
	pos int
	out strings.Builder
	m   Mapping
}

func NewBuilder(src string) *Builder {
	return &Builder{src: []rune(src)}
}

func (b *Builder) Copy(end int) {
	for i := b.pos; i < end; i++ {
		b.m.origStart = append(b.m.origStart, i)
		b.m.origEnd = append(b.m.origEnd, i+1)
	}
	b.out.WriteString(string(b.src[b.pos:end]))
	b.pos = end
}

// Replace stands repl in for the source up to end; a span touching repl maps
// back onto the whole replaced range.
func (b *Builder) Replace(end int, repl string) {
	for range repl {
		b.m.origStart = append(b.m.origStart, b.pos)
		b.m.origEnd = append(b.m.origEnd, end)
	}
	b.out.WriteString(repl)
	b.pos = end
}

// Insert adds text with no source counterpart; a span over it alone maps to
// nothing.
func (b *Builder) Insert(text string) {
	for range text {
		b.m.origStart = append(b.m.origStart, b.pos)
		b.m.origEnd = append(b.m.origEnd, b.pos)
	}
	b.out.WriteString(text)
}

// Skip leaves the source out up to end; no mapped-back span ever covers it.
func (b *Builder) Skip(end int) {
	if end > b.pos {
		b.m.skipped = append(b.m.skipped, Span{Start: b.pos, End: end})
	}
	b.pos = end
}

func (b *Builder) Len() int {
	return len(b.m.origStart)
}

// Build copies the rest of the source through first.
func (b *Builder) Build() (string, *Mapping) {
	b.Copy(len(b.src))
	return b.out.String(), &b.m
}

// Window clips spans to [start, end) and re-bases them to start.
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
