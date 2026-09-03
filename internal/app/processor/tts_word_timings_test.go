package processor

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"app/pkg/whisperx"

	"github.com/stretchr/testify/require"
)

func TestTimingsAreWordLevel(t *testing.T) {
	text := "hello chat, this is fine"
	words := []whisperx.Timiing{{Text: "hello"}, {Text: "chat,"}, {Text: "this"}, {Text: "is"}, {Text: "fine"}}
	require.True(t, timingsAreWordLevel(words, text))

	// engine sentence segments: several words per timing -> not word-level
	require.False(t, timingsAreWordLevel([]whisperx.Timiing{{Text: "hello chat, this is fine"}}, text))
	// count matches but words differ (display != spoken) -> not word-level
	off := append([]whisperx.Timiing(nil), words...)
	off[1] = whisperx.Timiing{Text: "chat"}
	require.False(t, timingsAreWordLevel(off, text))
	require.False(t, timingsAreWordLevel(nil, text))
	require.False(t, timingsAreWordLevel(words, ""))
}

func TestAlignChunkWordsUsesEngineWords(t *testing.T) {
	s := &Service{} // no audio aligner configured: engine words must suffice
	logger := slog.Default()
	spoken := "hello chat again"
	engine := []whisperx.Timiing{
		{Text: "hello", Start: 100 * time.Millisecond, End: 400 * time.Millisecond},
		{Text: "chat", Start: 450 * time.Millisecond, End: 700 * time.Millisecond},
		{Text: "again", Start: 800 * time.Millisecond, End: 1200 * time.Millisecond},
	}

	words := s.alignChunkWords(context.Background(), logger, spoken, spoken, nil, 1500*time.Millisecond, 0, 1500*time.Millisecond, engine)
	require.Len(t, words, 3)
	require.Equal(t, trackWord{W: "chat", S: 450, E: 700}, words[1])

	// display masked (ASCII art placeholder) -> engine words ignored, interpolation as before
	masked := s.alignChunkWords(context.Background(), logger, spoken, "hello [art] again", nil, 1500*time.Millisecond, 0, 1500*time.Millisecond, engine)
	require.Len(t, masked, 3)
	require.NotEqual(t, int64(450), masked[1].S)

	// word count mismatch (engine split differently) -> interpolation, never a panic
	short := s.alignChunkWords(context.Background(), logger, spoken, spoken, nil, 1500*time.Millisecond, 0, 1500*time.Millisecond, engine[:2])
	require.Len(t, short, 3)
}

func TestChunkLocalWords(t *testing.T) {
	require.Nil(t, chunkLocalWords(nil, time.Second))
	got := chunkLocalWords([]whisperx.Timiing{{Text: "a", Start: 3 * time.Second, End: 3500 * time.Millisecond}}, 2*time.Second)
	require.Equal(t, time.Second, got[0].Start)
	require.Equal(t, 1500*time.Millisecond, got[0].End)
}
