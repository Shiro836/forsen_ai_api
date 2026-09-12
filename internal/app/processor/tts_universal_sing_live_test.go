//go:build integration

package processor

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"app/db"
	"app/pkg/ai"
	"app/pkg/ffmpeg"
	"app/pkg/s3client"
	ttsprocessor "app/pkg/tts_processor"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// TestUniversalSingLive renders one message with a sung span through the real
// singer, IndexTTS, S3-backed voice cards and ffmpeg from cfg.yaml.
func TestUniversalSingLive(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	for _, bin := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not found in PATH: %v", bin, err)
		}
	}

	cfgBytes, cfgResolvedPath := mustReadCfg(t, agenticTurnsCfgPath)
	var c struct {
		DB       db.Config         `yaml:"db"`
		S3       s3client.Config   `yaml:"s3"`
		Ffmpeg   ffmpeg.Config     `yaml:"ffmpeg"`
		IndexTTS ai.IndexTTSConfig `yaml:"index_tts"`
		Singer   ai.SingerConfig   `yaml:"singer"`
		StyleTTS ai.StyleTTSConfig `yaml:"tts"`
	}
	require.NoError(t, yaml.Unmarshal(cfgBytes, &c), "failed to unmarshal cfg from %s", cfgResolvedPath)
	require.NotEmpty(t, c.Singer.URL, "singer.url not configured")

	database, err := db.New(ctx, &c.DB)
	require.NoError(t, err)
	s3, err := s3client.New(ctx, &c.S3)
	require.NoError(t, err)
	database.AttachS3Client(s3)

	tmpDir := strings.TrimSpace(c.Ffmpeg.TmpDir)
	if tmpDir == "" {
		tmpDir = os.TempDir()
	}
	ff := ffmpeg.New(&ffmpeg.Config{TmpDir: tmpDir})

	indexEngine := ai.NewIndexTTSEngine(ai.NewIndexTTSClient(nil, &c.IndexTTS), ff)
	singer := ai.NewSingerEngine(ai.NewSingerClient(nil, &c.Singer), indexEngine)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := NewService(logger, database, s3, ff, indexEngine, ai.NewStyleTTSClient(nil, &c.StyleTTS), singer, nil, nil, nil, nil)

	settings := &db.UserSettings{}
	msg := "hello chat {sing:jingle_bells} jingle bells jingle bells jingle all the way {.} and {sing:nope} bye"

	// SING_MESSAGE renders an arbitrary message instead (assertions on the
	// fixed one are skipped); SING_OUT writes the rendered audio there.
	if custom := os.Getenv("SING_MESSAGE"); custom != "" {
		tokens := svc.lexUniversal(ctx, settings, custom)
		audio, text, timings, err := svc.craftUniversalTTSAudio(ctx, slog.Default(), ttsprocessor.Actions(tokens, nil), settings)
		require.NoError(t, err)
		t.Logf("text=%q words=%d", text, len(timings))
		if out := os.Getenv("SING_OUT"); out != "" {
			require.NoError(t, os.WriteFile(out, audio, 0o644))
		}
		return
	}

	tokens := svc.lexUniversal(ctx, settings, msg)
	actions := ttsprocessor.Actions(tokens, nil)

	var sung int
	for _, a := range actions {
		if parseFilters(a.Filters).sing {
			sung++
			require.Equal(t, " jingle bells jingle bells jingle all the way ", a.Text)
		}
	}
	require.Equal(t, 1, sung, "exactly the known melody span is sung; {sing:nope} stays text")

	audio, text, timings, err := svc.craftUniversalTTSAudio(ctx, slog.Default(), actions, settings)
	require.NoError(t, err)
	require.Equal(t, "hello chat   jingle bells jingle bells jingle all the way   and {sing:nope} bye", text)

	probe, err := ff.Ffprobe(ctx, audio)
	require.NoError(t, err)
	t.Logf("duration=%s words=%d", probe.Duration, len(timings))
	require.Greater(t, probe.Duration, 8*time.Second, "spoken parts plus a sung jingle bells line")

	var words []string
	for i, tm := range timings {
		words = append(words, tm.Text)
		require.LessOrEqual(t, tm.Start, tm.End, "timing %d", i)
		if i > 0 {
			require.GreaterOrEqual(t, tm.Start, timings[i-1].Start, "timing %d out of order", i)
		}
	}
	require.Contains(t, strings.Join(words, " "), "jingle bells jingle bells jingle all the way")
	require.Contains(t, strings.Join(words, " "), "bye")
}
