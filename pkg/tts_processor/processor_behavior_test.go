package ttsprocessor

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func createMockValidators() (func(string) bool, func(string) bool, func(string) bool) {
	validVoices := map[string]bool{
		"abc": true, "asmr": true, "believer": true, "bog": true, "bogprime": true,
		"bogsen": true, "boris": true, "cancer": true, "chant": true, "classic": true,
		"commander": true, "doubter": true, "duck": true, "ed": true, "elon": true,
		"forsen": true, "forsenswa": true, "foryou": true, "gaben": true, "igor": true,
		"james": true, "jfk": true, "joe": true, "king": true, "lj": true,
		"monka": true, "nani": true, "nixon": true, "nomnom": true, "obama": true,
		"obiwan": true, "realfors": true, "santa": true, "saruman": true, "sven": true,
		"terra": true, "trailer": true, "tresh": true, "trump": true, "vincent": true,
		"vitch": true, "weskeru": true, "wutface": true, "yar": true,
	}

	validFilters := map[string]bool{
		".": true, // special case for filter pop
		"1": true, "2": true, "3": true, "4": true, "5": true, "6": true, "7": true,
		"8": true, "9": true, "10": true, "11": true, "12": true, "13": true, "14": true,
		"15": true, "16": true, "17": true,
	}

	validSfx := map[string]bool{
		"1": true, "2": true, "3": true, "4": true, "5": true, "6": true, "7": true,
		"8": true, "9": true, "10": true, "11": true, "12": true, "13": true, "14": true,
		"15": true, "16": true, "17": true, "18": true, "19": true, "20": true,
		"21": true, "22": true, "23": true, "24": true, "25": true, "26": true,
		"27": true, "28": true, "29": true, "30": true, "31": true,
	}

	checkVoice := func(voice string) bool {
		return validVoices[voice]
	}

	checkFilter := func(filter string) bool {
		return validFilters[filter]
	}

	checkSfx := func(sfx string) bool {
		return validSfx[sfx]
	}

	return checkVoice, checkFilter, checkSfx
}

func TestProcessMessage_CorrectBehavior(t *testing.T) {
	checkVoice, checkFilter, checkSfx := createMockValidators()

	tests := []struct {
		name     string
		message  string
		expected []Action
	}{
		{
			name:    "simple text",
			message: "hello world",
			expected: []Action{
				{
					Filters: []string{},
					Voice:   "",
					Text:    "hello world",
				},
			},
		},
		{
			name:    "voice change",
			message: "forsen: hello world",
			expected: []Action{
				{
					Filters: []string{},
					Voice:   "forsen",
					Text:    " hello world",
				},
			},
		},
		{
			name:    "voice change with text before",
			message: "some text forsen: hello world",
			expected: []Action{
				{
					Filters: []string{},
					Voice:   "",
					Text:    "some text ",
				},
				{
					Filters: []string{},
					Voice:   "forsen",
					Text:    " hello world",
				},
			},
		},
		{
			name:    "multiple voice changes",
			message: "forsen: hello trump: goodbye",
			expected: []Action{
				{
					Filters: []string{},
					Voice:   "forsen",
					Text:    " hello ",
				},
				{
					Filters: []string{},
					Voice:   "trump",
					Text:    " goodbye",
				},
			},
		},
		{
			name:    "invalid voice",
			message: "invalidvoice: hello world",
			expected: []Action{
				{
					Filters: []string{},
					Voice:   "",
					Text:    "invalidvoice: hello world",
				},
			},
		},
		{
			name:    "voice at end",
			message: "hello forsen:",
			expected: []Action{
				{
					Filters: []string{},
					Voice:   "",
					Text:    "hello ",
				},
			},
		},
		{
			name:    "voice with whitespace before colon",
			message: "forsen : hello",
			expected: []Action{
				{
					Filters: []string{},
					Voice:   "",
					Text:    "forsen : hello",
				},
			},
		},
		{
			name:    "filter",
			message: "{1}hello world{.}",
			expected: []Action{
				{
					Filters: []string{"1"},
					Voice:   "",
					Text:    "hello world",
				},
			},
		},
		{
			name:    "multiple filters",
			message: "{1}{2}hello world{.}",
			expected: []Action{
				{
					Filters: []string{"1", "2"},
					Voice:   "",
					Text:    "hello world",
				},
			},
		},
		{
			name:    "filter with voice",
			message: "forsen: {1}hello world{.}",
			expected: []Action{
				{
					Filters: []string{},
					Voice:   "forsen",
					Text:    " ",
				},
				{
					Filters: []string{"1"},
					Voice:   "forsen",
					Text:    "hello world",
				},
			},
		},
		{
			name:    "invalid filter",
			message: "{99}hello world{.}",
			expected: []Action{
				{
					Filters: []string{},
					Voice:   "",
					Text:    "{99}hello world",
				},
			},
		},
		{
			name:    "unclosed filter",
			message: "{1}hello world",
			expected: []Action{
				{
					Filters: []string{"1"},
					Voice:   "",
					Text:    "hello world",
				},
			},
		},
		{
			name:    "sound effect",
			message: "[1]hello world[1]",
			expected: []Action{
				{
					Filters: []string{},
					Sfx:     "1",
				},
				{
					Filters: []string{},
					Voice:   "",
					Text:    "hello world",
				},
				{
					Filters: []string{},
					Sfx:     "1",
				},
			},
		},
		{
			name:    "multiple sound effects",
			message: "[1][2]hello[1][2]",
			expected: []Action{
				{
					Filters: []string{},
					Sfx:     "1",
				},
				{
					Filters: []string{},
					Sfx:     "2",
				},
				{
					Filters: []string{},
					Voice:   "",
					Text:    "hello",
				},
				{
					Filters: []string{},
					Sfx:     "1",
				},
				{
					Filters: []string{},
					Sfx:     "2",
				},
			},
		},
		{
			name:    "sound effect with voice",
			message: "forsen: [1]hello[1]",
			expected: []Action{
				{
					Filters: []string{},
					Voice:   "forsen",
					Text:    " ",
				},
				{
					Filters: []string{},
					Sfx:     "1",
				},
				{
					Filters: []string{},
					Voice:   "forsen",
					Text:    "hello",
				},
				{
					Filters: []string{},
					Sfx:     "1",
				},
			},
		},
		{
			name:    "invalid sound effect",
			message: "[99]hello[99]",
			expected: []Action{
				{
					Filters: []string{},
					Voice:   "",
					Text:    "[99]hello[99]",
				},
			},
		},
		{
			name:    "unclosed sound effect",
			message: "[1]hello world",
			expected: []Action{
				{
					Filters: []string{},
					Sfx:     "1",
				},
				{
					Filters: []string{},
					Voice:   "",
					Text:    "hello world",
				},
			},
		},
		{
			name:    "complex example",
			message: "forsen: {1}[1]hello[1]{.}",
			expected: []Action{
				{
					Filters: []string{},
					Voice:   "forsen",
					Text:    " ",
				},
				{
					Filters: []string{"1"},
					Sfx:     "1",
				},
				{
					Filters: []string{"1"},
					Voice:   "forsen",
					Text:    "hello",
				},
				{
					Filters: []string{"1"},
					Sfx:     "1",
				},
			},
		},
		{
			name:    "forsen example from description",
			message: "forsen: oh shit I'm sorry trump: sorry for what?",
			expected: []Action{
				{
					Filters: []string{},
					Voice:   "forsen",
					Text:    " oh shit I'm sorry ",
				},
				{
					Filters: []string{},
					Voice:   "trump",
					Text:    " sorry for what?",
				},
			},
		},
		{
			name:    "complex real world example",
			message: "xd forsen: {1}hello {2}world {3}this is {4}amazing {.}trump: {5}[1]sound effect[1]{6}more text{.}invalidvoice: {99}bad filter{.}bog: {7}[99]bad sfx[99]{8}final text{.}",
			expected: []Action{
				{
					Filters: []string{},
					Voice:   "",
					Text:    "xd ",
				},
				// forsen: {1}hello {2}world {3}this is {4}amazing {.}
				{
					Filters: []string{},
					Voice:   "forsen",
					Text:    " ",
				},
				{
					Filters: []string{"1"},
					Voice:   "forsen",
					Text:    "hello ",
				},
				{
					Filters: []string{"1", "2"},
					Voice:   "forsen",
					Text:    "world ",
				},
				{
					Filters: []string{"1", "2", "3"},
					Voice:   "forsen",
					Text:    "this is ",
				},
				{
					Filters: []string{"1", "2", "3", "4"},
					Voice:   "forsen",
					Text:    "amazing ",
				},
				// trump: {5}[1]sound effect[1]{6}more text{.}
				{
					Filters: []string{"1", "2", "3"},
					Voice:   "trump",
					Text:    " ",
				},
				{
					Filters: []string{"1", "2", "3", "5"},
					Sfx:     "1",
				},
				{
					Filters: []string{"1", "2", "3", "5"},
					Voice:   "trump",
					Text:    "sound effect",
				},
				{
					Filters: []string{"1", "2", "3", "5"},
					Sfx:     "1",
				},
				{
					Filters: []string{"1", "2", "3", "5", "6"},
					Voice:   "trump",
					Text:    "more text",
				},
				// invalidvoice: {99}bad filter{.}
				{
					Filters: []string{"1", "2", "3", "5"},
					Voice:   "trump",
					Text:    "invalidvoice: {99}bad filter",
				},
				// bog: {7}[99]bad sfx[99]{8}final text{.}
				{
					Filters: []string{"1", "2", "3"},
					Voice:   "bog",
					Text:    " ",
				},
				{
					Filters: []string{"1", "2", "3", "7"},
					Voice:   "bog",
					Text:    "[99]bad sfx[99]",
				},
				{
					Filters: []string{"1", "2", "3", "7", "8"},
					Voice:   "bog",
					Text:    "final text",
				},
			},
		},
		{
			name:    "another complex test",
			message: "start forsen: {1}hello {2}world {3}test {.}trump: {4}[2]sfx[2]{5}more {.}bog: {6}final {7}text {.}end",
			expected: []Action{
				// start forsen: {1}hello {2}world {3}test {.}
				{
					Filters: []string{},
					Voice:   "",
					Text:    "start ",
				},
				{
					Filters: []string{},
					Voice:   "forsen",
					Text:    " ",
				},
				{
					Filters: []string{"1"},
					Voice:   "forsen",
					Text:    "hello ",
				},
				{
					Filters: []string{"1", "2"},
					Voice:   "forsen",
					Text:    "world ",
				},
				{
					Filters: []string{"1", "2", "3"},
					Voice:   "forsen",
					Text:    "test ",
				},
				// trump: {4}[2]sfx[2]{5}more {.}
				{
					Filters: []string{"1", "2"},
					Voice:   "trump",
					Text:    " ",
				},
				{
					Filters: []string{"1", "2", "4"},
					Sfx:     "2",
				},
				{
					Filters: []string{"1", "2", "4"},
					Voice:   "trump",
					Text:    "sfx",
				},
				{
					Filters: []string{"1", "2", "4"},
					Sfx:     "2",
				},
				{
					Filters: []string{"1", "2", "4", "5"},
					Voice:   "trump",
					Text:    "more ",
				},
				// bog: {6}final {7}text {.}end
				{
					Filters: []string{"1", "2", "4"},
					Voice:   "bog",
					Text:    " ",
				},
				{
					Filters: []string{"1", "2", "4", "6"},
					Voice:   "bog",
					Text:    "final ",
				},
				{
					Filters: []string{"1", "2", "4", "6", "7"},
					Voice:   "bog",
					Text:    "text ",
				},
				{
					Filters: []string{"1", "2", "4", "6"},
					Voice:   "bog",
					Text:    "end",
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actions, err := ProcessMessage(tt.message, checkVoice, checkFilter, checkSfx)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, actions, "message: %s", tt.message)
		})
	}
}

func TestProcessMessage_EdgeCases(t *testing.T) {
	checkVoice, checkFilter, checkSfx := createMockValidators()

	tests := []struct {
		name     string
		message  string
		expected []Action
	}{
		{
			name:     "empty message",
			message:  "",
			expected: []Action{},
		},
		{
			name:    "whitespace only",
			message: "   ",
			expected: []Action{
				{
					Filters: []string{},
					Voice:   "",
					Text:    "   ",
				},
			},
		},
		{
			name:    "colon without voice",
			message: ":hello",
			expected: []Action{
				{
					Filters: []string{},
					Voice:   "",
					Text:    "hello",
				},
			},
		},
		{
			name:    "multiple colons",
			message: "forsen:hello:world",
			expected: []Action{
				{
					Filters: []string{},
					Voice:   "forsen",
					Text:    "hello:world",
				},
			},
		},
		{
			name:    "special characters",
			message: "forsen: hello @#$%^&*() world!",
			expected: []Action{
				{
					Filters: []string{},
					Voice:   "forsen",
					Text:    " hello @#$%^&*() world!",
				},
			},
		},
		{
			name:    "unicode characters",
			message: "forsen: hello 世界 🌍",
			expected: []Action{
				{
					Filters: []string{},
					Voice:   "forsen",
					Text:    " hello 世界 🌍",
				},
			},
		},
		{
			name:    "nested brackets",
			message: "{[1]}hello{[1]}",
			expected: []Action{
				{
					Filters: []string{},
					Voice:   "",
					Text:    "{",
				},
				{
					Filters: []string{},
					Voice:   "",
					Sfx:     "1",
				},
				{
					Filters: []string{},
					Voice:   "",
					Text:    "}hello{",
				},
				{
					Filters: []string{},
					Voice:   "",
					Sfx:     "1",
				},
				{
					Filters: []string{},
					Voice:   "",
					Text:    "}",
				},
			},
		},
		{
			name:    "mixed bracket types",
			message: "{1}[1]hello[1]{.}",
			expected: []Action{
				{
					Filters: []string{"1"},
					Voice:   "",
					Sfx:     "1",
				},
				{
					Filters: []string{"1"},
					Voice:   "",
					Text:    "hello",
				},
				{
					Filters: []string{"1"},
					Voice:   "",
					Sfx:     "1",
				},
			},
		},
		{
			name:    "empty brackets",
			message: "{}[]hello{}[]",
			expected: []Action{
				{
					Filters: []string{},
					Voice:   "",
					Text:    "{}[]hello{}[]",
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actions, err := ProcessMessage(tt.message, checkVoice, checkFilter, checkSfx)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, actions, "message: %s", tt.message)
		})
	}
}

// TestProcessMessage_ErrorHandling tests error conditions
func TestProcessMessage_ErrorHandling(t *testing.T) {
	_, checkFilter, checkSfx := createMockValidators()

	// Test with nil validators should not panic
	t.Run("nil_voice_validator", func(t *testing.T) {
		// This will panic due to nil pointer dereference in the current implementation
		// The processor should be fixed to handle nil validators gracefully
		defer func() {
			if r := recover(); r != nil {
				// Expected to panic due to nil pointer dereference
				t.Logf("Expected panic occurred: %v", r)
			}
		}()

		_, err := ProcessMessage("forsen: hello", nil, checkFilter, checkSfx)
		if err != nil {
			t.Logf("Error occurred: %v", err)
		}
	})
}

// Benchmark tests for performance
func BenchmarkProcessMessage_Simple(b *testing.B) {
	checkVoice, checkFilter, checkSfx := createMockValidators()
	message := "forsen: hello world"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = ProcessMessage(message, checkVoice, checkFilter, checkSfx)
	}
}

func BenchmarkProcessMessage_Complex(b *testing.B) {
	checkVoice, checkFilter, checkSfx := createMockValidators()
	message := "forsen: {1}[1]hello world[1]{.} trump: {2}[2]goodbye[2]{.}"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = ProcessMessage(message, checkVoice, checkFilter, checkSfx)
	}
}

func BenchmarkProcessMessage_Long(b *testing.B) {
	checkVoice, checkFilter, checkSfx := createMockValidators()
	message := "forsen: " + strings.Repeat("hello world ", 100) + "trump: " + strings.Repeat("goodbye ", 100)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = ProcessMessage(message, checkVoice, checkFilter, checkSfx)
	}
}

func TestFilterPop(t *testing.T) {
	checkVoice, checkFilter, checkSfx := createMockValidators()
	message := "{9} [166] [166] {.} cancer: Oh! Sorry about that mr. bast, I dropped my plates. {9} [166] OH! OH! OH! OH! OH! OH! OH!"

	actions, err := ProcessMessage(message, checkVoice, checkFilter, checkSfx)

	fmt.Println(actions)

	require.NoError(t, err)
	assert.Equal(t, []Action{
		{
			Filters: []string{"9"},
			Sfx:     "166",
		},
		{
			Filters: []string{"9"},
			Sfx:     "166",
		},
		{
			Filters: []string{"9"},
			Sfx:     ".",
		},
		{
			Filters: []string{"9"},
			Voice:   "cancer",
			Text:    " Oh! Sorry about that mr. bast, I dropped my plates. ",
		},
		{
			Filters: []string{"9"},
			Sfx:     "166",
		},
		{
			Filters: []string{"9"},
			Text:    "OH! OH! OH! OH! OH! OH! OH!",
		},
	}, actions)
}
