package processor

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"app/db"
	"app/pkg/archive"
	"app/pkg/artfilter"
	"app/pkg/imagetag"
	"app/pkg/textfilter"
	ttsprocessor "app/pkg/tts_processor"
)

// FilterText censors with the regex patterns only. target is the archive's
// label for what is being filtered (archive.FilterTargetRequest/Reply).
func (s *Service) FilterText(ctx context.Context, userSettings *db.UserSettings, target, text string) string {
	spans := textfilter.Merge(s.regexSpans(userSettings, text))
	archive.RecordFilter(ctx, archive.FilterRun{Target: target, Regex: spans, Skipped: true})
	return textfilter.Censor(text, spans, "(filtered)")
}

// artPlaceholder stands in for character art on the overlay and in LLM filter
// input. The art itself still reaches TTS: hearing the engine mumble through a
// braille wall is a feature, showing the wall on stream is not.
const artPlaceholder = "(ascii art)"

// filterSpans marks text as it will be spoken: regex patterns plus the
// context-aware LLM filter. Character art is collapsed out of the LLM's input
// — a braille wall is ~10k tokens and breaks the echo protocol (measured:
// context overflow or a 60-100s retry stall). The caller archives the run
// (recordFilter) once the spans are mapped onto the raw message.
func (s *Service) filterSpans(ctx context.Context, userSettings *db.UserSettings, text string, skipLLM bool) (archive.FilterRun, error) {
	run := archive.FilterRun{Target: archive.FilterTargetRequest, Regex: s.regexSpans(userSettings, text), Skipped: skipLLM}
	if skipLLM {
		return run, nil
	}
	start := time.Now()
	llmInput, artMap := textfilter.Collapse(text, artfilter.Detect(text).Spans(text), artPlaceholder)
	llmInput, expandRepeats := textfilter.CollapseRepeats(llmInput)
	llmSpans, err := s.llmFilter.Spans(ctx, llmInput, userSettings.CustomFilterPrompt)
	if err != nil {
		return archive.FilterRun{}, err
	}
	run.LLM = artMap.MapBack(expandRepeats(llmSpans))
	run.LatencyMs = int(time.Since(start).Milliseconds())
	return run, nil
}

// filterReplySpans marks an AI reply, judging it against the prompt it answers
// so context-dependent hate ("I hate them") is caught.
func (s *Service) filterReplySpans(ctx context.Context, userSettings *db.UserSettings, prompt, reply string, skipLLM bool) (archive.FilterRun, error) {
	run := archive.FilterRun{Target: archive.FilterTargetReply, Regex: s.regexSpans(userSettings, reply), Skipped: skipLLM}
	if skipLLM {
		return run, nil
	}
	start := time.Now()
	prompt = artfilter.Detect(prompt).Mask(prompt, artPlaceholder)
	llmInput, artMap := textfilter.Collapse(reply, artfilter.Detect(reply).Spans(reply), artPlaceholder)
	llmInput, expandRepeats := textfilter.CollapseRepeats(llmInput)
	llmSpans, err := s.llmFilter.ReplySpans(ctx, prompt, llmInput, userSettings.CustomFilterPrompt)
	if err != nil {
		return archive.FilterRun{}, err
	}
	run.LLM = artMap.MapBack(expandRepeats(llmSpans))
	run.LatencyMs = int(time.Since(start).Milliseconds())
	return run, nil
}

func recordFilter(ctx context.Context, run archive.FilterRun, m *textfilter.Mapping) {
	run.Regex = m.MapBack(run.Regex)
	run.LLM = m.MapBack(run.LLM)
	archive.RecordFilter(ctx, run)
}

// trimBounds is strings.TrimSpace over r[start:end], as offsets.
func trimBounds(r []rune, start, end int) (int, int) {
	for start < end && unicode.IsSpace(r[start]) {
		start++
	}
	for end > start && unicode.IsSpace(r[end-1]) {
		end--
	}
	return start, end
}

func spokenRequest(prefix, message string) (string, *textfilter.Mapping) {
	r := []rune(message)
	lo, hi := trimBounds(r, 0, len(r))

	b := textfilter.NewBuilder(message)
	b.Insert(prefix)
	b.Skip(lo)
	for _, tag := range imagetag.Tags(message) {
		b.Copy(tag.Start)
		b.Replace(tag.End, tag.Placeholder)
	}
	b.Copy(hi)
	b.Skip(len(r))
	return b.Build()
}

// spokenUniversal leaves every tag out of the spoken text and trims it;
// ranges[i] is token i's place in it (empty for a tag).
func spokenUniversal(message string, tokens []ttsprocessor.Token) (spoken string, m *textfilter.Mapping, ranges []textfilter.Span) {
	r := []rune(message)
	lo, hi := 0, 0
	for _, tok := range tokens {
		if s, e := trimBounds(r, tok.Start, tok.End); tok.Kind == ttsprocessor.Text && s < e {
			if hi == 0 {
				lo = s
			}
			hi = e
		}
	}

	b := textfilter.NewBuilder(message)
	tags := imagetag.Tags(message)
	ranges = make([]textfilter.Span, len(tokens))
	for i, tok := range tokens {
		b.Skip(tok.Start)
		ranges[i].Start = b.Len()
		start, end := max(tok.Start, lo), min(tok.End, hi)
		if tok.Kind != ttsprocessor.Text || start >= end {
			b.Skip(tok.End)
			ranges[i].End = b.Len()
			continue
		}
		b.Skip(start)
		for _, tag := range tags {
			if tag.Start >= start && tag.End <= end {
				b.Copy(tag.Start)
				b.Replace(tag.End, tag.Placeholder)
			}
		}
		b.Copy(end)
		b.Skip(tok.End)
		ranges[i].End = b.Len()
	}
	b.Skip(len(r))
	spoken, m = b.Build()
	return spoken, m, ranges
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
