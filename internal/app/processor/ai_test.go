package processor

import (
	"reflect"
	"testing"
	"unicode/utf8"

	"app/pkg/textfilter"
)

func TestSpansAfterPrefix(t *testing.T) {
	// spans over "user asked me: <img:abcde> hi SLUR", filter tagged "SLUR".
	prefix := "user asked me: "
	body := "<img:abcde> hi SLUR"
	full := prefix + body
	slur := len(prefix + "<img:abcde> hi ")
	spans := []textfilter.Span{{
		Start: utf8.RuneCountInString(full[:slur]),
		End:   utf8.RuneCountInString(full),
	}}

	got := spansAfterPrefix(spans, utf8.RuneCountInString(prefix))
	want := []textfilter.Span{{
		Start: utf8.RuneCountInString(body[:len("<img:abcde> hi ")]),
		End:   utf8.RuneCountInString(body),
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("spansAfterPrefix = %+v, want %+v", got, want)
	}

	// The re-based span must select the slur within body alone.
	r := []rune(body)
	if string(r[got[0].Start:got[0].End]) != "SLUR" {
		t.Fatalf("re-based span selects %q, want %q", string(r[got[0].Start:got[0].End]), "SLUR")
	}
}

func TestSpansAfterPrefixDropsPrefixSpans(t *testing.T) {
	// A span wholly inside the prefix is dropped; a straddling one is clipped.
	spans := []textfilter.Span{{Start: 2, End: 5}, {Start: 8, End: 14}}
	got := spansAfterPrefix(spans, 10)
	want := []textfilter.Span{{Start: 0, End: 4}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("spansAfterPrefix = %+v, want %+v", got, want)
	}
}
