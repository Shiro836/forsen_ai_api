package processor

import (
	"reflect"
	"testing"
)

func TestParseFilters(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want actionFilters
	}{
		{
			name: "empty",
			in:   nil,
			want: actionFilters{},
		},
		{
			name: "mixed",
			in:   []string{"happy", "3", "old", "7"},
			want: actionFilters{emotions: []string{"happy"}, audioFilters: []string{"3", "7"}, oldTTS: true},
		},
		{
			name: "old is case-insensitive and not an audio filter",
			in:   []string{"OLD"},
			want: actionFilters{oldTTS: true},
		},
		{
			name: "no old flag",
			in:   []string{"sad", "2"},
			want: actionFilters{emotions: []string{"sad"}, audioFilters: []string{"2"}},
		},
		{
			name: "sing with melody keeps emotion as hint and audio filters",
			in:   []string{"sing:jingle_bells", "happy", "3"},
			want: actionFilters{emotions: []string{"happy"}, audioFilters: []string{"3"}, sing: true, melody: "jingle_bells"},
		},
		{
			name: "bare sing is random",
			in:   []string{"SING"},
			want: actionFilters{sing: true},
		},
		{
			name: "innermost sing wins, spaces kept",
			in:   []string{"sing:1", "sing: 2 "},
			want: actionFilters{sing: true, melody: " 2 "},
		},
		{
			name: "padded sing is not a sing filter",
			in:   []string{" sing"},
			want: actionFilters{audioFilters: []string{" sing"}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseFilters(tc.in); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("parseFilters(%v) = %+v, want %+v", tc.in, got, tc.want)
			}
		})
	}
}
