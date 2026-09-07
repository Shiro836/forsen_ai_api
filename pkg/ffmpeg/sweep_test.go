package ffmpeg

import (
	"testing"
	"time"
)

func TestSweepHzStaysInApulsatorRange(t *testing.T) {
	for _, dur := range []time.Duration{0, time.Millisecond, time.Second, 10 * time.Second, 68 * time.Second, time.Hour} {
		hz := SweepHz(dur)
		if hz < 0.01 || hz > 100 {
			t.Errorf("SweepHz(%v) = %v, outside apulsator range", dur, hz)
		}
	}
	if got := SweepHz(10 * time.Second); got != 0.05 {
		t.Errorf("SweepHz(10s) = %v, want 0.05", got)
	}
}
