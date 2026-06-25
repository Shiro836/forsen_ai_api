package processor

import "testing"

func TestStripForTTS(t *testing.T) {
	cases := map[string]string{
		"**Tip:** stay *calm*": "Tip: stay calm",
		"no asterisks here":     "no asterisks here",
		"*bites lip* hello":     "bites lip hello",
	}
	for in, want := range cases {
		if got := stripForTTS(in); got != want {
			t.Errorf("stripForTTS(%q) = %q, want %q", in, got, want)
		}
	}
}
