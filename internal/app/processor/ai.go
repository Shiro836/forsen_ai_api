package processor

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"app/db"
	"app/pkg/textfilter"
)

func (s *Service) FilterText(_ context.Context, userSettings *db.UserSettings, text string) string {
	spans := textfilter.Merge(s.regexSpans(userSettings, text))
	return textfilter.Censor(text, spans, "(filtered)")
}

// filterSpans marks a standalone message: regex patterns plus the context-aware
// LLM filter, merged.
func (s *Service) filterSpans(ctx context.Context, userSettings *db.UserSettings, text string, skipLLM bool) ([]textfilter.Span, error) {
	if skipLLM {
		return textfilter.Merge(s.regexSpans(userSettings, text)), nil
	}
	llmSpans, err := s.llmFilter.Spans(ctx, text, userSettings.CustomFilterPrompt)
	if err != nil {
		return nil, err
	}
	return textfilter.Merge(s.regexSpans(userSettings, text), llmSpans), nil
}

// filterReplySpans marks an AI reply, judging it against the prompt it answers
// so context-dependent hate ("I hate them") is caught.
func (s *Service) filterReplySpans(ctx context.Context, userSettings *db.UserSettings, prompt, reply string, skipLLM bool) ([]textfilter.Span, error) {
	if skipLLM {
		return textfilter.Merge(s.regexSpans(userSettings, reply)), nil
	}
	llmSpans, err := s.llmFilter.ReplySpans(ctx, prompt, reply, userSettings.CustomFilterPrompt)
	if err != nil {
		return nil, err
	}
	return textfilter.Merge(s.regexSpans(userSettings, reply), llmSpans), nil
}

// regexSpans returns the ranges matched by the built-in and per-user filter
// patterns, as rune offsets over text.
func (s *Service) regexSpans(userSettings *db.UserSettings, text string) []textfilter.Span {
	if userSettings.DisableRegexFilter {
		return nil
	}

	patterns := GlobalSwears
	if len(userSettings.Filters) != 0 {
		patterns = slices.Concat(patterns, strings.Split(userSettings.Filters, ","))
	}

	var spans []textfilter.Span
	for _, exp := range patterns {
		exp = strings.TrimSpace(exp)
		if exp == "" {
			continue
		}

		r, err := regexp.Compile("(?i)" + exp)
		if err != nil {
			s.logger.Warn(fmt.Sprintf("failed compiling reg expression '%s'", exp), "err", err)
			continue
		}
		for _, m := range r.FindAllStringIndex(text, -1) {
			spans = append(spans, textfilter.Span{
				Start: utf8.RuneCountInString(text[:m[0]]),
				End:   utf8.RuneCountInString(text[:m[1]]),
			})
		}
	}
	return spans
}
