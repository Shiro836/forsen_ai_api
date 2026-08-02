//go:build integration

package llmfilter_test

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestSpamIntegration covers targets the model cannot echo verbatim: long runs
// of a repeated token make it miscount or loop, so these exercise the substring
// listing fallback rather than the echo path.
func TestSpamIntegration(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		flagged []string
		clean   []string
	}{
		{
			name:  "repeated token spam is clean, not an error",
			input: "{angry} grace: " + strings.Repeat("steezi", 80) + "steez",
			clean: []string{"grace", "steezi"},
		},
		{
			name:    "slur buried in repeated token spam",
			input:   "{angry} grace: " + strings.Repeat("steezi", 20) + " neega " + strings.Repeat("steezi", 20),
			flagged: []string{"neega"},
		},
		{
			name:  "moderate repetition still echoes fine",
			input: "forsen: " + strings.Repeat("lul", 10),
			clean: []string{"lul"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
			defer cancel()

			spans, err := newFilter().Spans(ctx, tc.input, "")
			if err != nil {
				t.Fatalf("Spans: %v", err)
			}

			r := []rune(tc.input)
			for _, s := range spans {
				t.Logf("masked: %q", string(r[s.Start:s.End]))
			}
			for _, sub := range tc.flagged {
				if !flaggedAt(t, tc.input, spans, sub) {
					t.Errorf("expected %q to be flagged, spans=%v", sub, spans)
				}
			}
			for _, sub := range tc.clean {
				if flaggedAt(t, tc.input, spans, sub) {
					t.Errorf("expected %q NOT to be flagged, spans=%v", sub, spans)
				}
			}
		})
	}
}
