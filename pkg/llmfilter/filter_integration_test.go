//go:build integration

// The integration corpus for the LLM filter. It runs against a live provider
// (cfg oai, or oai_candidate when FILTER_CANDIDATE is set) and is split by
// theme: corpus_recall_test.go (what must be caught), corpus_precision_test.go
// (what must stay untouched), corpus_context_test.go (reply judged against its
// prompt), corpus_streamer_test.go (streamer rules), corpus_robustness_test.go
// (markup, spam, injection). Every case is a spanCase run through runSpanCases.
package llmfilter_test

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"app/cfg"
	"app/pkg/llmfilter"
	"app/pkg/oai"
	"app/pkg/textfilter"

	"gopkg.in/yaml.v3"
)

var testCfg *cfg.Config

// maskStats scores a run on precision, not just recall. Printed as one
// MASKSTATS line at exit for side-by-side provider comparison.
var maskStats struct {
	mu                      sync.Mutex
	cases, overBudget       int
	maskedRunes, totalRunes int
}

func recordMasking(total, masked int, overBudget bool) {
	maskStats.mu.Lock()
	defer maskStats.mu.Unlock()
	maskStats.cases++
	maskStats.totalRunes += total
	maskStats.maskedRunes += masked
	if overBudget {
		maskStats.overBudget++
	}
}

// inflight caps concurrent provider calls: subtests run in parallel, and a
// local llama-server has a handful of slots — more just queues and hits the
// per-case timeout. FILTER_PARALLEL overrides the default of 4.
var inflight chan struct{}

func TestMain(m *testing.M) {
	var cfgPath string
	flag.StringVar(&cfgPath, "cfg-path", "../../cfg/cfg.yaml", "path to config file")
	flag.Parse()

	cfgFile, err := os.ReadFile(cfgPath)
	if err != nil {
		log.Fatalf("can't open %s file: %v", cfgPath, err)
	}
	if err = yaml.Unmarshal(cfgFile, &testCfg); err != nil {
		log.Fatal("can't unmarshal cfg.yaml file", err)
	}

	par := 4
	if v, err := strconv.Atoi(os.Getenv("FILTER_PARALLEL")); err == nil && v > 0 {
		par = v
	}
	inflight = make(chan struct{}, par)

	code := m.Run()

	rate := 0.0
	if maskStats.totalRunes > 0 {
		rate = float64(maskStats.maskedRunes) / float64(maskStats.totalRunes)
	}
	fmt.Printf("MASKSTATS cases=%d over_budget=%d masked_runes=%d total_runes=%d mask_rate=%.4f\n",
		maskStats.cases, maskStats.overBudget, maskStats.maskedRunes, maskStats.totalRunes, rate)

	os.Exit(code)
}

// newFilter uses the cfg block named by FILTER_PROVIDER (oai, oai_candidate,
// filter_llm — the last is what production runs), defaulting to oai;
// FILTER_CANDIDATE=1 is shorthand for oai_candidate.
func newFilter() *llmfilter.Filter {
	c := testCfg.OAI
	switch os.Getenv("FILTER_PROVIDER") {
	case "oai_candidate":
		c = testCfg.OAICandidate
	case "filter_llm":
		c = testCfg.FilterLLM
	}
	if os.Getenv("FILTER_CANDIDATE") != "" {
		c = testCfg.OAICandidate
	}
	return llmfilter.New(oai.New(c.AccessToken, c.URL, c.Model, c.MaxTokens))
}

// spanCase is one corpus entry. input is the TARGET; context, when set, makes
// it a reply judged against that prompt; custom, when set, adds the streamer
// rules pass. flagged substrings must fall inside a span, clean ones must not,
// and maxMasked caps the total masked runes (0 = nothing may be masked, the
// right default for a clean case).
type spanCase struct {
	name      string
	input     string
	context   string
	custom    string
	flagged   []string
	anyOf     []string // at least one must be covered; for cases with several defensible spans
	clean     []string
	maxMasked int
}

func runSpanCases(t *testing.T, cases []spanCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			inflight <- struct{}{}
			defer func() { <-inflight }()

			ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
			defer cancel()

			f := newFilter()
			var (
				spans []textfilter.Span
				err   error
			)
			if tc.context != "" {
				spans, err = f.ReplySpans(ctx, tc.context, tc.input, tc.custom)
			} else {
				spans, err = f.Spans(ctx, tc.input, tc.custom)
			}
			if err != nil {
				t.Fatalf("filter: %v", err)
			}
			checkSpans(t, tc.input, spans, tc.flagged, tc.clean, tc.maxMasked)
			if len(tc.anyOf) > 0 {
				hit := false
				for _, sub := range tc.anyOf {
					if flaggedAt(t, tc.input, spans, sub) {
						hit = true
					}
				}
				if !hit {
					t.Errorf("expected one of %q to be flagged, spans=%v", tc.anyOf, spans)
				}
			}
		})
	}
}

// covers reports whether any span overlaps the [from, to) rune range.
func covers(spans []textfilter.Span, from, to int) bool {
	for _, s := range spans {
		if s.Start < to && from < s.End {
			return true
		}
	}
	return false
}

func runeIndex(text, sub string) int {
	r, n := []rune(text), []rune(sub)
	for i := 0; i+len(n) <= len(r); i++ {
		if string(r[i:i+len(n)]) == sub {
			return i
		}
	}
	return -1
}

// flaggedAt reports whether the substring sub (first occurrence) is covered by a span.
func flaggedAt(t *testing.T, text string, spans []textfilter.Span, sub string) bool {
	t.Helper()
	i := runeIndex(text, sub)
	if i < 0 {
		t.Fatalf("substring %q not in %q", sub, text)
	}
	return covers(spans, i, i+len([]rune(sub)))
}

// checkSpans scores one case. maxMasked is the over-filtering guard: without a
// cap, a model that masks the entire message satisfies every flagged assertion.
func checkSpans(t *testing.T, text string, spans []textfilter.Span, flagged, clean []string, maxMasked int) {
	t.Helper()

	r := []rune(text)
	masked := 0
	for _, s := range spans {
		if s.Start < 0 || s.End > len(r) || s.Start >= s.End {
			t.Fatalf("span %v out of bounds for input len %d", s, len(r))
		}
		masked += s.End - s.Start
		t.Logf("masked: %q", string(r[s.Start:s.End]))
	}

	over := masked > maxMasked
	if over {
		t.Errorf("over-filtered: masked %d of %d runes, budget %d", masked, len(r), maxMasked)
	}
	for _, sub := range flagged {
		if !flaggedAt(t, text, spans, sub) {
			t.Errorf("expected %q to be flagged, spans=%v", sub, spans)
		}
	}
	for _, sub := range clean {
		if flaggedAt(t, text, spans, sub) {
			t.Errorf("expected %q NOT to be flagged, spans=%v", sub, spans)
		}
	}

	recordMasking(len(r), masked, over)
}
