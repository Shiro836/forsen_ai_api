package processor

import (
	"testing"

	"app/db"
	ttsprocessor "app/pkg/tts_processor"
)

func TestUniversalJobsSingLimit(t *testing.T) {
	actions := []ttsprocessor.Action{
		{Text: "one", Filters: []string{"sing:1"}},
		{Text: "two", Filters: []string{"sing"}},
		{Text: "three", Filters: []string{"sing:2", "happy"}},
		{Text: "plain"},
	}

	cases := []struct {
		name  string
		limit *int
		sung  []bool
	}{
		{name: "default is two", limit: nil, sung: []bool{true, true, false, false}},
		{name: "explicit one", limit: ptr(1), sung: []bool{true, false, false, false}},
		{name: "zero is unlimited", limit: ptr(0), sung: []bool{true, true, true, false}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			jobs := (&Service{}).universalJobs(actions, &db.UserSettings{MaxSingCount: tc.limit})
			if len(jobs) != len(tc.sung) {
				t.Fatalf("got %d jobs, want %d", len(jobs), len(tc.sung))
			}
			for i, job := range jobs {
				if job.sing != tc.sung[i] {
					t.Errorf("job %d (%q) sing = %v, want %v", i, job.displayText, job.sing, tc.sung[i])
				}
			}
			if jobs[2].sing == false && jobs[2].ttsText == "three" {
				t.Errorf("over-limit span lost its emotion hint: ttsText = %q", jobs[2].ttsText)
			}
		})
	}
}

func ptr(n int) *int { return &n }
