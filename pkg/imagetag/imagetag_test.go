package imagetag

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExtractIDs(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   string
		max  int
		want []string
	}{
		{
			name: "no matches returns nil",
			in:   "hello world",
			max:  0,
			want: nil,
		},
		{
			name: "single match",
			in:   "see this <img:abc12> ok",
			max:  0,
			want: []string{"abc12"},
		},
		{
			name: "multiple matches preserve order",
			in:   "<img:abc12> then <img:Z9yX0>",
			max:  0,
			want: []string{"abc12", "Z9yX0"},
		},
		{
			name: "max cap",
			in:   "<img:abc12> <img:Z9yX0> <img:QQ111>",
			max:  2,
			want: []string{"abc12", "Z9yX0"},
		},
		{
			name: "max <= 0 means all",
			in:   "<img:abc12> <img:Z9yX0>",
			max:  -1,
			want: []string{"abc12", "Z9yX0"},
		},
		{
			name: "ignores invalid tag length",
			in:   "<img:abcd> <img:abc12>",
			max:  0,
			want: []string{"abc12"},
		},
		{
			name: "ignores invalid characters (non-alnum)",
			in:   "<img:ab-12> <img:abC12>",
			max:  0,
			want: []string{"abC12"},
		},
		{
			name: "preview link",
			in:   "check forsen.fun/i/abc12 out",
			max:  0,
			want: []string{"abc12"},
		},
		{
			name: "raw image link",
			in:   "check forsen.fun/images/Z9yX0 out",
			max:  0,
			want: []string{"Z9yX0"},
		},
		{
			name: "mixed tag and links preserve order",
			in:   "<img:abc12> forsen.fun/i/def34 forsen.fun/images/gh567",
			max:  0,
			want: []string{"abc12", "def34", "gh567"},
		},
		{
			name: "link code longer than five chars ignored",
			in:   "forsen.fun/i/abc123",
			max:  0,
			want: nil,
		},
		{
			name: "other host ignored",
			in:   "example.com/i/abc12",
			max:  0,
			want: nil,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := ExtractIDs(tc.in, tc.max)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestReplaceID(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   string
		id   string
		repl string
		want string
	}{
		{
			name: "tag form",
			in:   "look <img:abc12> ok",
			id:   "abc12",
			repl: "",
			want: "look  ok",
		},
		{
			name: "preview link form",
			in:   "look forsen.fun/i/abc12 ok",
			id:   "abc12",
			repl: "(desc)",
			want: "look (desc) ok",
		},
		{
			name: "raw link form",
			in:   "look forsen.fun/images/abc12 ok",
			id:   "abc12",
			repl: "(desc)",
			want: "look (desc) ok",
		},
		{
			name: "only first occurrence",
			in:   "<img:abc12> forsen.fun/i/abc12",
			id:   "abc12",
			repl: "X",
			want: "X forsen.fun/i/abc12",
		},
		{
			name: "absent id unchanged",
			in:   "nothing here",
			id:   "abc12",
			repl: "X",
			want: "nothing here",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := ReplaceID(tc.in, tc.id, tc.repl)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestReplaceImageTags(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "no tags unchanged",
			in:   "hello",
			want: "hello",
		},
		{
			name: "one tag",
			in:   "look <img:abc12> ok",
			want: "look image_1 ok",
		},
		{
			name: "two tags",
			in:   "<img:abc12> and <img:Z9yX0>",
			want: "image_1 and image_2",
		},
		{
			name: "invalid tag unchanged",
			in:   "bad <img:abcd> ok",
			want: "bad <img:abcd> ok",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := ReplaceImageTags(tc.in)
			require.Equal(t, tc.want, got)
		})
	}
}
