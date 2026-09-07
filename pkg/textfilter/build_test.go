package textfilter

import (
	"reflect"
	"testing"
)

func TestBuilderInsertHasNoSource(t *testing.T) {
	// derived: "user asked me: hi SLUR"   source: "hi SLUR"
	derived, m := func() (string, *Mapping) {
		b := NewBuilder("hi SLUR")
		b.Insert("user asked me: ")
		return b.Build()
	}()
	if derived != "user asked me: hi SLUR" {
		t.Fatalf("derived = %q", derived)
	}

	// a span over the insert alone maps to nothing
	if back := m.MapBack([]Span{{0, 4}}); back != nil {
		t.Errorf("insert-only span = %v, want none", back)
	}
	// a span over the body lands on the body
	if back := m.MapBack([]Span{{18, 22}}); !reflect.DeepEqual(back, []Span{{3, 7}}) {
		t.Errorf("body span = %v, want [{3 7}]", back)
	}
	// a span reaching from the insert into the body keeps the body part
	if back := m.MapBack([]Span{{0, 17}}); !reflect.DeepEqual(back, []Span{{0, 2}}) {
		t.Errorf("straddling span = %v, want [{0 2}]", back)
	}
}

func TestBuilderSkipIsNeverCovered(t *testing.T) {
	// source: "bad cancer: words"  derived: "bad  words" (tag skipped)
	src := "bad cancer: words"
	b := NewBuilder(src)
	b.Copy(4)
	b.Skip(11)
	derived, m := b.Build()
	if derived != "bad  words" {
		t.Fatalf("derived = %q", derived)
	}

	// a span across the gap is cut around the skipped tag
	back := m.MapBack([]Span{{0, 10}})
	want := []Span{{0, 4}, {11, 17}}
	if !reflect.DeepEqual(back, want) {
		t.Errorf("span across gap = %v, want %v", back, want)
	}
	if got := Censor(src, back, "(f)"); got != "(f)cancer:(f)" {
		t.Errorf("censor = %q", got)
	}
}

func TestBuilderReplaceExpands(t *testing.T) {
	src := "see <img:abcde> now"
	b := NewBuilder(src)
	b.Copy(4)
	b.Replace(15, "image_1")
	derived, m := b.Build()
	if derived != "see image_1 now" {
		t.Fatalf("derived = %q", derived)
	}
	if back := m.MapBack([]Span{{6, 9}}); !reflect.DeepEqual(back, []Span{{4, 15}}) {
		t.Errorf("span inside placeholder = %v, want the whole tag", back)
	}
}

func TestBuilderLen(t *testing.T) {
	b := NewBuilder("abc def")
	b.Insert("xx")
	b.Copy(3)
	if b.Len() != 5 {
		t.Fatalf("Len = %d, want 5", b.Len())
	}
	b.Skip(4)
	if b.Len() != 5 {
		t.Fatalf("Len after skip = %d, want 5", b.Len())
	}
}

func TestWindow(t *testing.T) {
	spans := []Span{{2, 5}, {8, 14}, {20, 22}}
	got := Window(spans, 4, 12)
	want := []Span{{0, 1}, {4, 8}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Window = %v, want %v", got, want)
	}
	if got := Window(spans, 15, 19); got != nil {
		t.Fatalf("empty window = %v", got)
	}
}
