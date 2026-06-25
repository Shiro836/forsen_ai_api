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
