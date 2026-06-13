package ai_test

import (
	"testing"

	"app/pkg/ai"

	"github.com/stretchr/testify/require"
)

func TestInsertExtractEmotions(t *testing.T) {
	t.Parallel()

	text, emotions := ai.ExtractEmotions(ai.InsertEmotions("I FUCKING HATE THEM!", []string{"angry"}))
	require.Equal(t, "I FUCKING HATE THEM!", text)
	require.Equal(t, []string{"angry"}, emotions)

	text, emotions = ai.ExtractEmotions(ai.InsertEmotions("I didn't save him", []string{"angry", "sad"}))
	require.Equal(t, "I didn't save him", text)
	require.Equal(t, []string{"angry", "sad"}, emotions)
}

func TestInsertEmotionsNoValidTags(t *testing.T) {
	t.Parallel()

	require.Equal(t, "hello", ai.InsertEmotions("hello", nil))
	require.Equal(t, "hello", ai.InsertEmotions("hello", []string{"rage", ""}))
}

func TestExtractEmotionsPlainText(t *testing.T) {
	t.Parallel()

	text, emotions := ai.ExtractEmotions("just a normal message")
	require.Equal(t, "just a normal message", text)
	require.Empty(t, emotions)
}

func TestInsertEmotionsDedupes(t *testing.T) {
	t.Parallel()

	_, emotions := ai.ExtractEmotions(ai.InsertEmotions("text", []string{"sad", "sad", "happy"}))
	require.Equal(t, []string{"sad", "happy"}, emotions)
}

func TestEmotionsCaseInsensitive(t *testing.T) {
	t.Parallel()

	require.True(t, ai.IsEmotion("ANGRY"))
	require.True(t, ai.IsEmotion("Sad"))

	// {ANGRY}{angry} collapses to one lowercase tag
	_, emotions := ai.ExtractEmotions(ai.InsertEmotions("text", []string{"ANGRY", "angry"}))
	require.Equal(t, []string{"angry"}, emotions)

	require.Equal(t, []float64{0, 1, 0, 0, 0, 0, 0, 0}, ai.EmotionVector([]string{"ANGRY"}))
}

func TestEmotionVector(t *testing.T) {
	t.Parallel()

	require.Nil(t, ai.EmotionVector(nil))
	require.Nil(t, ai.EmotionVector([]string{"not-an-emotion"}))

	// single emotion gets full weight, capped at 1.0
	vec := ai.EmotionVector([]string{"angry"})
	require.Equal(t, []float64{0, 1, 0, 0, 0, 0, 0, 0}, vec)

	// two emotions split the budget
	vec = ai.EmotionVector([]string{"angry", "sad"})
	require.Equal(t, []float64{0, 0.65, 0.65, 0, 0, 0, 0, 0}, vec)
}
