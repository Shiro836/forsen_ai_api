package processor

import (
	"testing"

	"app/pkg/whisperx"
)

func TestSegmentsCoverWords(t *testing.T) {
	words := []string{"Hello", "chat", "this", "is", "a", "test.", "Second", "sentence."}

	cases := []struct {
		name     string
		segments []whisperx.Timiing
		want     bool
	}{
		{
			name: "exact partition",
			segments: []whisperx.Timiing{
				{Text: "Hello chat this is a test."},
				{Text: "Second sentence."},
			},
			want: true,
		},
		{
			name:     "single segment covering all",
			segments: []whisperx.Timiing{{Text: "Hello chat this is a test. Second sentence."}},
			want:     true,
		},
		{
			name:     "no segments",
			segments: nil,
			want:     false,
		},
		{
			name: "engine rewrote text (hard-split normalization)",
			segments: []whisperx.Timiing{
				{Text: "HELLO CHAT THIS IS A TEST."},
				{Text: "Second sentence."},
			},
			want: false,
		},
		{
			name: "segments miss trailing words",
			segments: []whisperx.Timiing{
				{Text: "Hello chat this is a test."},
			},
			want: false,
		},
		{
			name: "segments have extra words",
			segments: []whisperx.Timiing{
				{Text: "Hello chat this is a test."},
				{Text: "Second sentence. Third one."},
			},
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := segmentsCoverWords(tc.segments, words); got != tc.want {
				t.Errorf("segmentsCoverWords() = %v, want %v", got, tc.want)
			}
		})
	}
}
