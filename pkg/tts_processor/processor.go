// Package ttsprocessor lexes a universal TTS message into spoken text and the
// inline tags that shape it: `name:` switches voice, `{name}` pushes a filter
// (`{.}` pops), `[name]` plays a sound. A candidate the check callbacks reject
// stays text and is spoken as written.
package ttsprocessor

import "slices"

type Kind int

const (
	Text Kind = iota
	Voice
	Filter
	Sfx
)

// Token is one lexed piece of a message; Start and End are rune offsets.
type Token struct {
	Kind  Kind
	Start int
	End   int
	Value string
}

type Action struct {
	Filters []string

	Voice string
	Text  string

	Sfx string
}

// Lex splits message into text and recognized tags, in message order. A `:`
// with nothing pending before it belongs to no token.
func Lex(message string, checkVoice func(string) bool, checkFilter func(string) bool, checkSfx func(string) bool) []Token {
	r := []rune(message)
	var tokens []Token
	start := 0

	text := func(from, to int) {
		if to > from {
			tokens = append(tokens, Token{Kind: Text, Start: from, End: to, Value: string(r[from:to])})
		}
	}

	for i, chr := range r {
		switch chr {
		case ']', '}':
			opening := '['
			kind, check := Sfx, checkSfx
			if chr == '}' {
				opening = '{'
				kind, check = Filter, checkFilter
			}

			open := lastIndex(r[start:i], opening)
			if open == -1 {
				continue
			}
			open += start

			content := string(r[open+1 : i])
			if !check(content) {
				continue
			}

			text(start, open)
			tokens = append(tokens, Token{Kind: kind, Start: open, End: i + 1, Value: content})
			start = i + 1

		case ':':
			if start == i {
				start = i + 1
				continue
			}

			nameStart := lastIndex(r[start:i], ' ')
			if nameStart == -1 {
				nameStart = start
			} else {
				nameStart += start + 1
			}

			name := string(r[nameStart:i])
			if !checkVoice(name) {
				continue
			}

			text(start, nameStart)
			tokens = append(tokens, Token{Kind: Voice, Start: nameStart, End: i + 1, Value: name})
			start = i + 1
		}
	}

	text(start, len(r))
	return tokens
}

func lastIndex(r []rune, c rune) int {
	for i := len(r) - 1; i >= 0; i-- {
		if r[i] == c {
			return i
		}
	}
	return -1
}

// Actions folds tokens into playback actions. render supplies the spoken form
// of text token i; nil speaks it as written.
func Actions(tokens []Token, render func(i int) string) []Action {
	filters := []string{}
	voice := ""
	actions := []Action{}

	for i, tok := range tokens {
		switch tok.Kind {
		case Text:
			text := tok.Value
			if render != nil {
				text = render(i)
			}
			actions = append(actions, Action{Filters: slices.Clone(filters), Voice: voice, Text: text})
		case Voice:
			voice = tok.Value
		case Filter:
			if tok.Value == "." {
				if len(filters) != 0 {
					filters = filters[:len(filters)-1]
				}
			} else {
				filters = append(filters, tok.Value)
			}
		case Sfx:
			actions = append(actions, Action{Filters: slices.Clone(filters), Sfx: tok.Value})
		}
	}

	return actions
}

func ProcessMessage(message string, checkVoice func(string) bool, checkFilter func(string) bool, checkSfx func(string) bool) []Action {
	return Actions(Lex(message, checkVoice, checkFilter, checkSfx), nil)
}
