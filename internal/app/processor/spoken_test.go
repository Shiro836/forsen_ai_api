package processor

import (
	"reflect"
	"testing"

	"app/pkg/textfilter"
	ttsprocessor "app/pkg/tts_processor"
)

func TestSpokenRequestPrefixAndImages(t *testing.T) {
	raw := "<img:abcde> hi SLUR"
	spoken, m := spokenRequest("user asked me: ", raw)
	if spoken != "user asked me: image_1 hi SLUR" {
		t.Fatalf("spoken = %q", spoken)
	}

	name := textfilter.Span{Start: 0, End: 4}
	slur := textfilter.Span{Start: 26, End: 30}

	if got := textfilter.Censor(spoken, []textfilter.Span{name, slur}, "(f)"); got != "(f) asked me: image_1 hi (f)" {
		t.Errorf("censored speech = %q", got)
	}

	back := m.MapBack([]textfilter.Span{name, slur})
	want := []textfilter.Span{{Start: 15, End: 19}}
	if !reflect.DeepEqual(back, want) {
		t.Errorf("MapBack = %v, want %v", back, want)
	}
	if got := textfilter.Censor(raw, back, "(f)"); got != "<img:abcde> hi (f)" {
		t.Errorf("raw censor = %q", got)
	}

	if got := textfilter.Window([]textfilter.Span{name, slur}, 0, 4); !reflect.DeepEqual(got, []textfilter.Span{{Start: 0, End: 4}}) {
		t.Errorf("requester spans = %v", got)
	}
}

func TestSpokenUniversalLeavesTagsOut(t *testing.T) {
	raw := "{9} cancer: bad word <img:abcde> [166] tail"
	voices := map[string]bool{"cancer": true}
	tokens := ttsprocessor.Lex(raw,
		func(v string) bool { return voices[v] },
		func(f string) bool { return f == "9" },
		func(s string) bool { return s == "166" })

	spoken, m, ranges := spokenUniversal(raw, tokens)
	if spoken != "bad word image_1  tail" {
		t.Fatalf("spoken = %q", spoken)
	}

	spans := []textfilter.Span{{Start: 0, End: 8}}

	back := m.MapBack(spans)
	if !reflect.DeepEqual(back, []textfilter.Span{{Start: 12, End: 20}}) {
		t.Errorf("MapBack = %v", back)
	}

	spokenRunes := []rune(spoken)
	actions := ttsprocessor.Actions(tokens, func(i int) string {
		r := ranges[i]
		return textfilter.Censor(string(spokenRunes[r.Start:r.End]), textfilter.Window(spans, r.Start, r.End), "(f)")
	})
	want := []ttsprocessor.Action{
		{Filters: []string{"9"}},
		{Filters: []string{"9"}, Voice: "cancer", Text: "(f) image_1 "},
		{Filters: []string{"9"}, Sfx: "166"},
		{Filters: []string{"9"}, Voice: "cancer", Text: " tail"},
	}
	if !reflect.DeepEqual(actions, want) {
		t.Errorf("actions = %+v, want %+v", actions, want)
	}
}

func TestSpokenTrimsPadding(t *testing.T) {
	spoken, m := spokenRequest("", "  hi SLUR \n")
	if spoken != "hi SLUR" {
		t.Fatalf("spoken = %q", spoken)
	}
	if back := m.MapBack([]textfilter.Span{{Start: 3, End: 7}}); !reflect.DeepEqual(back, []textfilter.Span{{Start: 5, End: 9}}) {
		t.Errorf("MapBack = %v", back)
	}

	raw := "{sing:1}goatis: I eat raw meat {.}"
	tokens := ttsprocessor.Lex(raw,
		func(v string) bool { return v == "goatis" },
		func(f string) bool { return f == "sing:1" || f == "." },
		func(string) bool { return false })
	spoken, m, ranges := spokenUniversal(raw, tokens)
	if spoken != "I eat raw meat" {
		t.Fatalf("spoken = %q", spoken)
	}
	if back := m.MapBack([]textfilter.Span{{Start: 10, End: 14}}); !reflect.DeepEqual(back, []textfilter.Span{{Start: 26, End: 30}}) {
		t.Errorf("MapBack = %v", back)
	}
	if got := ranges[2]; got != (textfilter.Span{Start: 0, End: 14}) {
		t.Errorf("text range = %v", got)
	}
}

func TestSpokenUniversalSpanAcrossTagNeverCoversIt(t *testing.T) {
	raw := "bad cancer: word"
	tokens := ttsprocessor.Lex(raw,
		func(v string) bool { return v == "cancer" },
		func(string) bool { return false },
		func(string) bool { return false })

	spoken, m, _ := spokenUniversal(raw, tokens)
	if spoken != "bad  word" {
		t.Fatalf("spoken = %q", spoken)
	}

	back := m.MapBack([]textfilter.Span{{Start: 0, End: 9}})
	want := []textfilter.Span{{Start: 0, End: 4}, {Start: 11, End: 16}}
	if !reflect.DeepEqual(back, want) {
		t.Errorf("MapBack = %v, want %v", back, want)
	}
}
