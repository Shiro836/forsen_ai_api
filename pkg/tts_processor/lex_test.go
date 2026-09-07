package ttsprocessor

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLexOffsets(t *testing.T) {
	checkVoice, checkFilter, checkSfx := createMockValidators()

	tests := []struct {
		name    string
		message string
		want    []Token
	}{
		{
			name:    "voice tag with text on both sides",
			message: "hi forsen: hello",
			want: []Token{
				{Kind: Text, Start: 0, End: 3, Value: "hi "},
				{Kind: Voice, Start: 3, End: 10, Value: "forsen"},
				{Kind: Text, Start: 10, End: 16, Value: " hello"},
			},
		},
		{
			name:    "unrecognized voice stays text",
			message: "hitler: say something",
			want: []Token{
				{Kind: Text, Start: 0, End: 21, Value: "hitler: say something"},
			},
		},
		{
			name:    "filter and sfx tags",
			message: "{9} [166] x {.}",
			want: []Token{
				{Kind: Filter, Start: 0, End: 3, Value: "9"},
				{Kind: Text, Start: 3, End: 4, Value: " "},
				{Kind: Sfx, Start: 4, End: 9, Value: "166"},
				{Kind: Text, Start: 9, End: 12, Value: " x "},
				{Kind: Filter, Start: 12, End: 15, Value: "."},
			},
		},
		{
			name:    "leading colon belongs to no token",
			message: ":hello",
			want: []Token{
				{Kind: Text, Start: 1, End: 6, Value: "hello"},
			},
		},
		{
			name:    "rune offsets past multibyte text",
			message: "héllo forsen: 世界",
			want: []Token{
				{Kind: Text, Start: 0, End: 6, Value: "héllo "},
				{Kind: Voice, Start: 6, End: 13, Value: "forsen"},
				{Kind: Text, Start: 13, End: 16, Value: " 世界"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, Lex(tt.message, checkVoice, checkFilter, checkSfx))
		})
	}
}

func TestActionsRender(t *testing.T) {
	checkVoice, checkFilter, checkSfx := createMockValidators()
	tokens := Lex("forsen: bad [1] fine", checkVoice, checkFilter, checkSfx)

	actions := Actions(tokens, func(i int) string {
		if tokens[i].Value == " bad " {
			return " (f) "
		}
		return tokens[i].Value
	})

	assert.Equal(t, []Action{
		{Filters: []string{}, Voice: "forsen", Text: " (f) "},
		{Filters: []string{}, Sfx: "1"},
		{Filters: []string{}, Voice: "forsen", Text: " fine"},
	}, actions)
}
