package textfilter

import "testing"

func TestMerge(t *testing.T) {
	tests := []struct {
		name string
		sets [][]Span
		want []Span
	}{
		{
			name: "empty",
			sets: nil,
			want: nil,
		},
		{
			name: "sorts and keeps disjoint",
			sets: [][]Span{{{5, 7}}, {{0, 2}}},
			want: []Span{{0, 2}, {5, 7}},
		},
		{
			name: "coalesces overlap",
			sets: [][]Span{{{2, 6}}, {{4, 10}}},
			want: []Span{{2, 10}},
		},
		{
			name: "coalesces touching",
			sets: [][]Span{{{0, 3}}, {{3, 5}}},
			want: []Span{{0, 5}},
		},
		{
			name: "keeps contained within",
			sets: [][]Span{{{0, 10}}, {{3, 5}}},
			want: []Span{{0, 10}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Merge(tc.sets...)
			if len(got) != len(tc.want) {
				t.Fatalf("Merge = %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("Merge = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

func TestCensor(t *testing.T) {
	tests := []struct {
		name  string
		text  string
		spans []Span
		want  string
	}{
		{"none", "hello world", nil, "hello world"},
		{"one", "I hate jews", []Span{{2, 6}}, "I (f) jews"},
		{"multiple", "a bad b bad c", []Span{{2, 5}, {8, 11}}, "a (f) b (f) c"},
		{"non-ascii offsets", "café hate x", []Span{{5, 9}}, "café (f) x"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Censor(tc.text, tc.spans, "(f)"); got != tc.want {
				t.Fatalf("Censor = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCollapseIdentityWithoutSpans(t *testing.T) {
	text := "nothing to collapse"
	got, m := Collapse(text, nil, "(art)")
	if got != text || m != nil {
		t.Fatalf("got %q, mapping %v", got, m)
	}
	spans := []Span{{2, 5}}
	if back := m.MapBack(spans); back[0] != spans[0] {
		t.Errorf("nil mapping must be identity, got %v", back)
	}
}

func TestCollapseMapBack(t *testing.T) {
	// runes:      0123456789...
	text := "abc ⣿⣿⣿⣿⣿ hate xyz"
	collapsed, m := Collapse(text, []Span{{4, 9}}, "(art)")
	if collapsed != "abc (art) hate xyz" {
		t.Fatalf("collapsed = %q", collapsed)
	}

	// "hate" in collapsed [10,14) -> original [10,14)
	back := m.MapBack([]Span{{10, 14}})
	if len(back) != 1 || back[0].Start != 10 || back[0].End != 14 {
		t.Errorf("back = %v", back)
	}
	if got := Censor(text, back, "(f)"); got != "abc ⣿⣿⣿⣿⣿ (f) xyz" {
		t.Errorf("censor = %q", got)
	}
}

func TestCollapseMapBackThroughPlaceholder(t *testing.T) {
	text := "aa ⣿⣿⣿ bb"
	collapsed, m := Collapse(text, []Span{{3, 6}}, "(art)")
	if collapsed != "aa (art) bb" {
		t.Fatalf("collapsed = %q", collapsed)
	}

	// span covering the placeholder expands to the full replaced range
	back := m.MapBack([]Span{{3, 8}})
	if len(back) != 1 || back[0].Start != 3 || back[0].End != 6 {
		t.Errorf("back = %v", back)
	}

	// span straddling placeholder into trailing text
	back = m.MapBack([]Span{{3, 11}})
	if len(back) != 1 || back[0].Start != 3 || back[0].End != 9 {
		t.Errorf("straddling back = %v", back)
	}
}
