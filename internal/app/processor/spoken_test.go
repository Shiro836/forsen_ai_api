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

	// spans over the spoken text, as the filter returns them
	name := textfilter.Span{Start: 0, End: 4}
	slur := textfilter.Span{Start: 26, End: 30}

	if got := textfilter.Censor(spoken, []textfilter.Span{name, slur}, "(f)"); got != "(f) asked me: image_1 hi (f)" {
		t.Errorf("censored speech = %q", got)
	}

	// the panel sees the slur on the raw message and nothing for the name
	back := m.MapBack([]textfilter.Span{name, slur})
	want := []textfilter.Span{{Start: 15, End: 19}}
	if !reflect.DeepEqual(back, want) {
		t.Errorf("MapBack = %v, want %v", back, want)
	}
	if got := textfilter.Censor(raw, back, "(f)"); got != "<img:abcde> hi (f)" {
		t.Errorf("raw censor = %q", got)
	}

	// the name's own spans, for the requested-by cell
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
	if spoken != "  bad word image_1  tail" {
		t.Fatalf("spoken = %q", spoken)
	}

	// filter flags "bad word" in the spoken text
	spans := []textfilter.Span{{Start: 2, End: 10}}

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
		{Filters: []string{"9"}, Text: " "},
		{Filters: []string{"9"}, Voice: "cancer", Text: " (f) image_1 "},
		{Filters: []string{"9"}, Sfx: "166"},
		{Filters: []string{"9"}, Voice: "cancer", Text: " tail"},
	}
	if !reflect.DeepEqual(actions, want) {
		t.Errorf("actions = %+v, want %+v", actions, want)
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
