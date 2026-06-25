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
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseFilters(tc.in); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("parseFilters(%v) = %+v, want %+v", tc.in, got, tc.want)
			}
		})
	}
}
