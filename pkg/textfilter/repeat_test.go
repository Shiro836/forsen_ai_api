package textfilter

import (
	"strings"
	"testing"
)

func TestCollapseRepeats(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"no repeats", "hello there general kenobi", "hello there general kenobi"},
		{"three repeats stay", "lul lul lul", "lul lul lul"},
		{"spaced unit", strings.Repeat("stizi my nikah ", 50) + "ok", strings.Repeat("stizi my nikah ", 3) + "ok"},
		{"unspaced kana", strings.Repeat("ブラック・ニカ", 71), strings.Repeat("ブラック・ニカ", 3)},
		{"prefix and suffix kept", "forsen: " + strings.Repeat("你可以 ", 17) + "bye", "forsen: " + strings.Repeat("你可以 ", 3) + "bye"},
		{"two runs", strings.Repeat("ab ", 10) + "and " + strings.Repeat("cd ", 10), strings.Repeat("ab ", 3) + "and " + strings.Repeat("cd ", 3)},
		{"letter spam collapses by pairs", strings.Repeat("A", 40), strings.Repeat("A", 6)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, _ := CollapseRepeats(tc.in)
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCollapseRepeatsExpand(t *testing.T) {
	in := "forsen: " + strings.Repeat("你可以 ", 17) + "bye"
	collapsed, expand := CollapseRepeats(in)

	// a span over the second kept copy widens to the whole run; the run's
	// unit is " 你可以" since the prefix ends in a space
	spans := expand([]Span{{Start: len([]rune("forsen: ")) + 4, End: len([]rune("forsen: ")) + 7}})
	if len(spans) != 1 {
		t.Fatalf("got %v", spans)
	}
	got := string([]rune(in)[spans[0].Start:spans[0].End])
	if strings.TrimSpace(got) != strings.TrimSpace(strings.Repeat("你可以 ", 17)) {
		t.Fatalf("expanded span covers %q", got)
	}

	// a span outside the run maps through unchanged
	spans = expand([]Span{{Start: 0, End: 6}})
	if len(spans) != 1 || string([]rune(in)[spans[0].Start:spans[0].End]) != "forsen" {
		t.Fatalf("got %v", spans)
	}
	tail := len([]rune(collapsed))
	spans = expand([]Span{{Start: tail - 3, End: tail}})
	if len(spans) != 1 || string([]rune(in)[spans[0].Start:spans[0].End]) != "bye" {
		t.Fatalf("got %v", spans)
	}
}
