package ffmpeg_test

import (
	"app/pkg/ffmpeg"
	"context"
	"os"
	"testing"
	"time"

	_ "embed"

	"github.com/stretchr/testify/require"
)

func TestFfprobePath(t *testing.T) {
	// Skip test if reference file doesn't exist
	if _, err := os.Stat("okayeg_ref.wav"); os.IsNotExist(err) {
		t.Skip("reference audio file not found")
	}

	assert := require.New(t)
	client := ffmpeg.New(&ffmpeg.Config{
		TmpDir: "/tmp",
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	res, err := client.FfprobePath(ctx, "okayeg_ref.wav")
	assert.NoError(err)

	assert.NotEmpty(res.Duration)

	// fmt.Println(res)
}

//go:embed okayeg_ref.wav
var audio []byte

func TestFfprobe(t *testing.T) {
	assert := require.New(t)
	client := ffmpeg.New(&ffmpeg.Config{
		TmpDir: "/tmp",
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	res, err := client.Ffprobe(ctx, audio)
	assert.NoError(err)

	assert.NotEmpty(res.Duration)
}

func TestFfmpegPath(t *testing.T) {
	// Skip test if reference file doesn't exist
	if _, err := os.Stat("okayeg_ref.wav"); os.IsNotExist(err) {
		t.Skip("reference audio file not found")
	}

	assert := require.New(t)
	client := ffmpeg.New(&ffmpeg.Config{
		TmpDir: "/tmp",
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	res, err := client.Ffmpeg2Mp3Path(ctx, "okayeg_ref.wav")
	assert.NoError(err)

	assert.NotEmpty(res)

	// Create tmp directory if it doesn't exist
	os.MkdirAll("tmp", 0755)
	err = os.WriteFile("tmp/out.mp3", res, 0644)
	assert.NoError(err)
}

func TestFfmpeg(t *testing.T) {
	assert := require.New(t)
	client := ffmpeg.New(&ffmpeg.Config{
		TmpDir: "/tmp",
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	res, err := client.Ffmpeg2Mp3(ctx, audio)
	assert.NoError(err)

	assert.NotEmpty(res)

	// Create tmp directory if it doesn't exist
	os.MkdirAll("tmp", 0755)
	err = os.WriteFile("tmp/out.mp3", res, 0644)
	assert.NoError(err)
}

func TestConcatenateAudio2Files(t *testing.T) {
	assert := require.New(t)
	client := ffmpeg.New(&ffmpeg.Config{
		TmpDir: "/tmp",
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Concatenate 2 copies of the same audio file with no padding
	result, err := client.ConcatenateAudio(ctx, 0, audio, audio)
	assert.NoError(err)
	assert.NotEmpty(result)

	// Check duration of concatenated result using ffprobe
	resultDuration, err := client.Ffprobe(ctx, result)
	assert.NoError(err)

	// Get original duration for comparison
	originalDuration, err := client.FfprobePath(ctx, "okayeg_ref.wav")
	assert.NoError(err)

	// The concatenated audio should be approximately 2x the original duration
	expectedDuration := originalDuration.Duration * 2
	tolerance := 500 * time.Millisecond // Allow 500ms tolerance
	assert.InDelta(float64(expectedDuration.Milliseconds()), float64(resultDuration.Duration.Milliseconds()), float64(tolerance.Milliseconds()))

	// Create tmp directory if it doesn't exist
	os.MkdirAll("tmp", 0755)
	// Save the result for inspection
	err = os.WriteFile("tmp/concatenated_2.mp3", result, 0644)
	assert.NoError(err)
}

func TestConcatenateAudio3Files(t *testing.T) {
	assert := require.New(t)
	client := ffmpeg.New(&ffmpeg.Config{
		TmpDir: "/tmp",
	})

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Concatenate 3 copies of the same audio file with no padding
	result, err := client.ConcatenateAudio(ctx, 0, audio, audio, audio)
	assert.NoError(err)
	assert.NotEmpty(result)

	// Check duration of concatenated result using ffprobe
	resultDuration, err := client.Ffprobe(ctx, result)
	assert.NoError(err)

	// Get original duration for comparison
	originalDuration, err := client.FfprobePath(ctx, "okayeg_ref.wav")
	assert.NoError(err)

	// The concatenated audio should be approximately 3x the original duration
	expectedDuration := originalDuration.Duration * 3
	tolerance := 500 * time.Millisecond // Allow 500ms tolerance
	assert.InDelta(float64(expectedDuration.Milliseconds()), float64(resultDuration.Duration.Milliseconds()), float64(tolerance.Milliseconds()))

	// Create tmp directory if it doesn't exist
	os.MkdirAll("tmp", 0755)
	// Save the result for inspection
	err = os.WriteFile("tmp/concatenated_3.mp3", result, 0644)
	assert.NoError(err)
}

func TestConcatenateAudioWithPadding(t *testing.T) {
	assert := require.New(t)
	client := ffmpeg.New(&ffmpeg.Config{
		TmpDir: "/tmp",
	})

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Get original duration for comparison
	originalDuration, err := client.FfprobePath(ctx, "okayeg_ref.wav")
	assert.NoError(err)

	// Concatenate 3 copies with 1 second padding
	result, err := client.ConcatenateAudio(ctx, 2*time.Second, audio, audio, audio)
	assert.NoError(err)
	assert.NotEmpty(result)

	// Check duration of concatenated result using ffprobe
	resultDuration, err := client.Ffprobe(ctx, result)
	assert.NoError(err)

	// For 3 audio files with 2 seconds padding between each:
	// audio1 + 2s silence + audio2 + 2s silence + audio3
	expectedDuration := originalDuration.Duration*3 + 2*2*time.Second
	tolerance := 500 * time.Millisecond // Allow 500ms tolerance
	assert.InDelta(float64(expectedDuration.Milliseconds()), float64(resultDuration.Duration.Milliseconds()), float64(tolerance.Milliseconds()))

	// Create tmp directory if it doesn't exist
	os.MkdirAll("tmp", 0755)
	// Save the result for inspection
	err = os.WriteFile("tmp/concatenated_with_padding.mp3", result, 0644)
	assert.NoError(err)
}

func TestConcatenateAudio2FilesWithPadding(t *testing.T) {
	assert := require.New(t)
	client := ffmpeg.New(&ffmpeg.Config{
		TmpDir: "/tmp",
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Get original duration for comparison
	originalDuration, err := client.FfprobePath(ctx, "okayeg_ref.wav")
	assert.NoError(err)

	// Concatenate 2 copies with 500ms padding
	result, err := client.ConcatenateAudio(ctx, 500*time.Millisecond, audio, audio)
	assert.NoError(err)
	assert.NotEmpty(result)

	// Check duration of concatenated result using ffprobe
	resultDuration, err := client.Ffprobe(ctx, result)
	assert.NoError(err)

	// For 2 audio files with 500ms padding between each:
	// audio1 + 500ms silence + audio2
	expectedDuration := originalDuration.Duration*2 + 500*time.Millisecond
	tolerance := 500 * time.Millisecond // Allow 500ms tolerance
	assert.InDelta(float64(expectedDuration.Milliseconds()), float64(resultDuration.Duration.Milliseconds()), float64(tolerance.Milliseconds()))

	// Create tmp directory if it doesn't exist
	os.MkdirAll("tmp", 0755)
	// Save the result for inspection
	err = os.WriteFile("tmp/concatenated_2_with_padding.mp3", result, 0644)
	assert.NoError(err)
}

func TestConcatenateAudio4FilesWithPadding(t *testing.T) {
	assert := require.New(t)
	client := ffmpeg.New(&ffmpeg.Config{
		TmpDir: "/tmp",
	})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// Get original duration for comparison
	originalDuration, err := client.FfprobePath(ctx, "okayeg_ref.wav")
	assert.NoError(err)

	// Concatenate 4 copies with 1.5 seconds padding
	result, err := client.ConcatenateAudio(ctx, 1500*time.Millisecond, audio, audio, audio, audio)
	assert.NoError(err)
	assert.NotEmpty(result)

	// Check duration of concatenated result using ffprobe
	resultDuration, err := client.Ffprobe(ctx, result)
	assert.NoError(err)

	// For 4 audio files with 1.5s padding between each:
	// audio1 + 1.5s silence + audio2 + 1.5s silence + audio3 + 1.5s silence + audio4
	expectedDuration := originalDuration.Duration*4 + 3*1500*time.Millisecond
	tolerance := 500 * time.Millisecond // Allow 500ms tolerance
	assert.InDelta(float64(expectedDuration.Milliseconds()), float64(resultDuration.Duration.Milliseconds()), float64(tolerance.Milliseconds()))

	// Create tmp directory if it doesn't exist
	os.MkdirAll("tmp", 0755)
	// Save the result for inspection
	err = os.WriteFile("tmp/concatenated_4_with_padding.mp3", result, 0644)
	assert.NoError(err)
}
