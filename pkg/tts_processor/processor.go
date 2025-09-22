package ttsprocessor

import (
	"fmt"
	"slices"
	"strings"
)

type Action struct {
	Filters []string

	Voice string
	Text  string

	Sfx string
}

func ProcessMessage(message string, checkVoice func(string) bool, checkFilter func(string) bool, checkSfx func(string) bool) ([]Action, error) {
	curFilters := []string{}
	curVoice := ""

	s := strings.Builder{}

	actions := []Action{}

	for _, chr := range message {
		switch chr {
		case ']', '}':
			openingBracket := '['
			if chr == '}' {
				openingBracket = '{'
			}

			text := s.String()

			openPosition := strings.LastIndex(text, string(openingBracket))
			if openPosition == -1 {
				_, err := s.WriteRune(chr)
				if err != nil {
					return nil, fmt.Errorf("error writing to string builder: %w", err)
				}

				continue
			}

			content := text[openPosition+1:]
			if openingBracket == '[' && checkSfx(content) {
				text = text[:openPosition]
				if len(text) > 0 {
					actions = append(actions, Action{
						Filters: slices.Clone(curFilters),
						Voice:   curVoice,
						Text:    text,
					})
				}

				actions = append(actions, Action{
					Filters: slices.Clone(curFilters),
					Sfx:     content,
				})

				s.Reset()

				continue
			}

			if openingBracket == '{' && checkFilter(content) {
				text = text[:openPosition]

				if len(text) > 0 {
					actions = append(actions, Action{
						Filters: slices.Clone(curFilters),
						Voice:   curVoice,
						Text:    text,
					})
				}

				s.Reset()

				if content == "." {
					if len(curFilters) != 0 {
						curFilters = curFilters[:len(curFilters)-1]
					}
				} else {
					curFilters = append(curFilters, content)
				}

				continue
			}

			_, err := s.WriteRune(chr)
			if err != nil {
				return nil, fmt.Errorf("error writing to string builder: %w", err)
			}
		case ':':
			text := s.String()

			newVoice := ""

			if len(text) == 0 {
				continue
			}

			// find last whitespace in text
			lastWhitespace := strings.LastIndex(text, " ")
			if lastWhitespace == -1 {
				newVoice = text
				if checkVoice(newVoice) {
					curVoice = newVoice
					s.Reset()

					continue
				}

				_, err := s.WriteRune(chr)
				if err != nil {
					return nil, fmt.Errorf("error writing to string builder: %w", err)
				}

				continue
			}

			if lastWhitespace != len(text)-1 {
				newVoice = text[lastWhitespace+1:]
			}

			if checkVoice(newVoice) {
				text = text[:lastWhitespace+1]

				if len(text) > 0 {
					actions = append(actions, Action{
						Filters: slices.Clone(curFilters),
						Voice:   curVoice,
						Text:    text,
					})
				}
				curVoice = newVoice

				s.Reset()

				continue
			}

			fallthrough

		default:
			_, err := s.WriteRune(chr)
			if err != nil {
				return nil, fmt.Errorf("error writing to string builder: %w", err)
			}
		}
	}

	if s.Len() > 0 {
		actions = append(actions, Action{
			Filters: curFilters,
			Voice:   curVoice,
			Text:    s.String(),
		})

		s.Reset()
	}

	return actions, nil
}
