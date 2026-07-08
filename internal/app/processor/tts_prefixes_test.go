package processor

import (
	"testing"
	"time"

	"app/pkg/whisperx"

	"github.com/stretchr/testify/require"
)

func TestTimingTextPrefixes(t *testing.T) {
	t.Parallel()

	msg := "Hello chat, welcome back. Today we play the worst game ever. Wish me luck."

	timings := []whisperx.Timiing{
		{Text: "HELLO CHAT,WELCOME BACK.", Start: 0, End: 2 * time.Second},
		{Text: "TODAY WE PLAY THE WORST GAME EVER.", Start: 2 * time.Second, End: 5 * time.Second},
		{Text: "WISH ME LUCK.", Start: 5 * time.Second, End: 6 * time.Second},
	}

	prefixes := timingTextPrefixes(msg, timings)
	require.Len(t, prefixes, 3)

	// each prefix extends the previous one and ends on a word boundary
	require.True(t, len(prefixes[0]) < len(prefixes[1]))
	require.True(t, len(prefixes[1]) < len(prefixes[2]))
	for _, p := range prefixes[:2] {
		require.True(t, len(p) > 0)
		require.Equal(t, p, msg[:len(p)])
	}

	// the last prefix is always the full message
	require.Equal(t, msg, prefixes[2])
}

func TestTimingTextPrefixesWordDiffs(t *testing.T) {
	t.Parallel()

	msg := "Hello chat, welcome back everyone"

	timings := make([]whisperx.Timiing, 0, 5)
	for i, w := range []string{"Hello", "chat,", "welcome", "back", "everyone"} {
		timings = append(timings, whisperx.Timiing{
			Text:  w,
			Start: time.Duration(i) * time.Second,
			End:   time.Duration(i+1) * time.Second,
		})
	}

	prefixes := timingTextPrefixes(msg, timings)
	require.Len(t, prefixes, 5)
	require.Equal(t, msg, prefixes[4])

	// every diff must start with the word itself, not a space: the overlay
	// typewriter renders only the first diff token synchronously, so a
	// leading space costs the word when events arrive faster than 200ms
	prev := ""
	for _, p := range prefixes {
		require.True(t, len(p) >= len(prev))
		require.Equal(t, prev, p[:len(prev)])
		diff := p[len(prev):]
		if len(diff) > 0 {
			require.NotEqual(t, byte(' '), diff[0], "diff %q starts with a space", diff)
		}
		prev = p
	}
}

func TestTimingTextPrefixesEmpty(t *testing.T) {
	t.Parallel()

	require.Nil(t, timingTextPrefixes("hello", nil))

	// degenerate timing text still yields the full message at the end
	prefixes := timingTextPrefixes("hello world", []whisperx.Timiing{{Text: ""}})
	require.Equal(t, []string{"hello world"}, prefixes)
}
