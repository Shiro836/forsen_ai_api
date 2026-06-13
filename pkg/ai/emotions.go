package ai

import (
	"slices"
	"strings"
)

// emotionIndex maps each universal-TTS emotion tag to its position in the
// 8-dimensional IndexTTS emotion vector.
var emotionIndex = map[string]int{
	"happy":       0,
	"angry":       1,
	"sad":         2,
	"afraid":      3,
	"disgusted":   4,
	"melancholic": 5,
	"surprised":   6,
	"calm":        7,
}

// EmotionNames lists the supported emotion tags in vector order.
var EmotionNames = []string{"happy", "angry", "sad", "afraid", "disgusted", "melancholic", "surprised", "calm"}

// IsEmotion reports whether name is a supported emotion tag (case-insensitive).
func IsEmotion(name string) bool {
	_, ok := emotionIndex[strings.ToLower(name)]
	return ok
}

const (
	emotionMarkerStart = "\x01emo:"
	emotionMarkerEnd   = "\x01"

	// emotionWeightBudget caps the summed vector weight: the model blends the
	// vector with the voice's natural emotion by (1 - sum), so sums well past 1
	// actively subtract the natural tone and get unnatural fast.
	emotionWeightBudget = 1.3
)

// InsertEmotions encodes emotion tags into the text as an inline marker so the
// message travels through the engine-agnostic TTS path unchanged. Engines
// decode it with ExtractEmotions; unknown names are dropped.
func InsertEmotions(text string, emotions []string) string {
	valid := dedupeEmotions(emotions)
	if len(valid) == 0 {
		return text
	}

	return emotionMarkerStart + strings.Join(valid, ",") + emotionMarkerEnd + text
}

// ExtractEmotions strips the marker produced by InsertEmotions and returns the
// clean text together with the encoded emotion tags.
func ExtractEmotions(text string) (string, []string) {
	rest, ok := strings.CutPrefix(text, emotionMarkerStart)
	if !ok {
		return text, nil
	}

	names, cleanText, ok := strings.Cut(rest, emotionMarkerEnd)
	if !ok {
		return text, nil
	}

	return cleanText, dedupeEmotions(strings.Split(names, ","))
}

// EmotionVector builds the 8-dim IndexTTS emotion vector for the given tags,
// splitting the weight budget evenly. Returns nil if no valid tags are given.
func EmotionVector(emotions []string) []float64 {
	valid := dedupeEmotions(emotions)
	if len(valid) == 0 {
		return nil
	}

	weight := emotionWeightBudget / float64(len(valid))
	if weight > 1 {
		weight = 1
	}

	vec := make([]float64, len(emotionIndex))
	for _, name := range valid {
		vec[emotionIndex[name]] = weight
	}

	return vec
}

func dedupeEmotions(emotions []string) []string {
	var out []string
	for _, name := range emotions {
		name = strings.ToLower(name)
		if !IsEmotion(name) || slices.Contains(out, name) {
			continue
		}

		out = append(out, name)
	}

	return out
}
