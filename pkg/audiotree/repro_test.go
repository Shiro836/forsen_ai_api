package audiotree

import (
	"context"
	"os"
	"os/exec"
	"path"
	"regexp"
	"strconv"
	"testing"
	"time"

	"app/pkg/ffmpeg"
)

func meanVolume(t *testing.T, audio []byte) float64 {
	t.Helper()
	file := path.Join(t.TempDir(), "vol")
	if err := os.WriteFile(file, audio, 0644); err != nil {
		t.Fatal(err)
	}

	out, err := exec.Command("ffmpeg", "-i", file, "-af", "volumedetect", "-f", "null", "-").CombinedOutput()
	if err != nil {
		t.Fatalf("volumedetect: %v", err)
	}

	match := regexp.MustCompile(`mean_volume: (-?[\d.]+) dB`).FindSubmatch(out)
	if match == nil {
		t.Fatalf("no mean_volume in output:\n%s", out)
	}
	vol, err := strconv.ParseFloat(string(match[1]), 64)
	if err != nil {
		t.Fatal(err)
	}
	return vol
}

// A deep same-span filter stack must process in the same order through both
// renderers (regression for a prod message where the tree applied the stack
// reversed, pitching the background sounds into rumble). Loudness diverges by
// design: legacy compresses at every pass because each MP3 intermediate can
// clip, while the tree's float graph limits once at the end — it comes out a
// few dB louder on hot stacks, and loudnorm levels the final output anyway.
// An order bug would show as tens of dB or a duration shift.
func TestDeepStackMatchesLegacy(t *testing.T) {
	client := ffmpeg.New(&ffmpeg.Config{TmpDir: t.TempDir()})
	ctx := context.Background()
	clip := sine(t, 3*time.Second)
	stack := []string{"2", "4", "4", "4", "21", "21", "21", "24", "25", "25", "25"}

	segments := []Segment{{Audio: clip, Filters: stack, Duration: 3 * time.Second}}

	legacyAudio, _, err := NewLegacy(client).Render(ctx, segments, 500*time.Millisecond, false)
	if err != nil {
		t.Fatal(err)
	}

	treeAudio, _, err := New(client).Render(ctx, segments, 500*time.Millisecond, false)
	if err != nil {
		t.Fatal(err)
	}

	legacyVol := meanVolume(t, legacyAudio)
	treeVol := meanVolume(t, treeAudio)
	t.Logf("mean volume: legacy %.1f dB, tree %.1f dB", legacyVol, treeVol)

	if diff := legacyVol - treeVol; diff > 8 || diff < -8 {
		t.Errorf("renderers diverge: legacy %.1f dB vs tree %.1f dB", legacyVol, treeVol)
	}
}
