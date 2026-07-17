package ffmpeg

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// for the purpose of compressing audio to the highest degree
func (c *Client) Ffmpeg2Mp3Path(ctx context.Context, inputPath string, disableLimiter bool) ([]byte, error) {
	outputPath := path.Join(c.cfg.TmpDir, prefix+uuid.NewString())

	defer os.Remove(outputPath)

	args := []string{"-i", inputPath, "-nostats", "-loglevel", "0"}
	if !disableLimiter {
		args = append(args, "-af", "alimiter=limit=0.9:attack=5:release=50")
	}
	args = append(args, "-ar", "44100", "-ac", "2", "-b:a", "192k", "-vn", "-f", "mp3", outputPath)

	cmd := exec.CommandContext(ctx, "ffmpeg", args...)

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("failed to run ffmpeg: %w", err)
	}

	output, err := os.ReadFile(outputPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read output file: %w", err)
	}

	return output, nil
}

func (c *Client) Ffmpeg2Mp3(ctx context.Context, data []byte, disableLimiter bool) ([]byte, error) {
	path := path.Join(c.cfg.TmpDir, prefix+uuid.NewString())

	err := os.WriteFile(path, data, 0644)
	if err != nil {
		return nil, fmt.Errorf("write file: %w", err)
	}

	defer os.Remove(path)

	return c.Ffmpeg2Mp3Path(ctx, path, disableLimiter)
}

// ConcatenateAudio concatenates multiple audio files into one using ffmpeg
// padding specifies the silence duration between audio files
func (c *Client) ConcatenateAudio(ctx context.Context, padding time.Duration, audioFiles ...[]byte) ([]byte, error) {
	if len(audioFiles) == 0 {
		return nil, fmt.Errorf("no audio files to concatenate")
	}
	if len(audioFiles) == 1 {
		return audioFiles[0], nil
	}

	tempFiles := make([]string, len(audioFiles))
	defer func() {
		for _, tempFile := range tempFiles {
			if tempFile != "" {
				os.Remove(tempFile)
			}
		}
	}()

	for i, audioData := range audioFiles {
		tempFile := path.Join(c.cfg.TmpDir, prefix+uuid.NewString())
		err := os.WriteFile(tempFile, audioData, 0644)
		if err != nil {
			return nil, fmt.Errorf("write temp file %d: %w", i, err)
		}
		tempFiles[i] = tempFile
	}

	outputPath := path.Join(c.cfg.TmpDir, prefix+uuid.NewString()+".mp3")
	defer os.Remove(outputPath)

	// concat filter instead of the concat demuxer: handles mismatched codecs/rates
	args := []string{
		"-i", tempFiles[0], // First input
	}

	// Add additional inputs
	for i := 1; i < len(tempFiles); i++ {
		args = append(args, "-i", tempFiles[i])
	}

	// Add filter complex for concatenation with padding
	filterComplex := ""

	// If padding is specified, add silence padding between audio files
	if padding > 0 {
		// Convert duration to seconds for anullsrc
		paddingSecs := padding.Seconds()

		// Create unique silence segments for each gap
		for i := 1; i < len(tempFiles); i++ {
			filterComplex += fmt.Sprintf("anullsrc=channel_layout=stereo:sample_rate=44100,atrim=duration=%.3f[silence%d];", paddingSecs, i)
		}

		// Build the concatenation: [0:a] + silence1 + [1:a] + silence2 + [2:a] + ...
		filterComplex += "[0:a]"
		for i := 1; i < len(tempFiles); i++ {
			filterComplex += fmt.Sprintf("[silence%d][%d:a]", i, i)
		}

		// Total inputs: original audio files + silence between them
		totalInputs := len(tempFiles) + len(tempFiles) - 1
		filterComplex += fmt.Sprintf("concat=n=%d:v=0:a=1[out]", totalInputs)
	} else {
		// No padding, use simple concatenation
		for i := 0; i < len(tempFiles); i++ {
			filterComplex += fmt.Sprintf("[%d:a]", i)
		}
		filterComplex += fmt.Sprintf("concat=n=%d:v=0:a=1[out]", len(tempFiles))
	}

	args = append(args,
		"-filter_complex", filterComplex,
		"-map", "[out]",
		"-c:a", "mp3",
		"-b:a", "192k",
		"-ar", "44100",
		"-ac", "2",
		"-y", // Overwrite output file
		outputPath,
	)

	cmd := exec.CommandContext(ctx, "ffmpeg", args...)

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("failed to run ffmpeg concatenation: %w", err)
	}

	output, err := os.ReadFile(outputPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read concatenated output file: %w", err)
	}

	return output, nil
}

func (c *Client) CutAudio(ctx context.Context, data []byte, maxDuration time.Duration) ([]byte, error) {
	inputPath := path.Join(c.cfg.TmpDir, prefix+uuid.NewString())
	outputPath := path.Join(c.cfg.TmpDir, prefix+uuid.NewString()+".mp3")

	defer os.Remove(inputPath)
	defer os.Remove(outputPath)

	err := os.WriteFile(inputPath, data, 0644)
	if err != nil {
		return nil, fmt.Errorf("write input file: %w", err)
	}

	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-i", inputPath,
		"-t", fmt.Sprintf("%.3f", maxDuration.Seconds()),
		"-c", "copy", // Copy without re-encoding for speed
		"-y", // Overwrite output file
		outputPath)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("failed to run ffmpeg cut: %w, stderr: %s", err, stderr.String())
	}

	output, err := os.ReadFile(outputPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read cut output file: %w", err)
	}

	return output, nil
}

// LoudnessStats are loudnorm's measured input values. Feeding them back as
// the filter's measured_* parameters (linear mode) applies one consistent
// gain, so many clips normalized against the same stats keep their relative
// dynamics — unlike per-clip NormalizeAudio.
type LoudnessStats struct {
	I      string `json:"input_i"`
	TP     string `json:"input_tp"`
	LRA    string `json:"input_lra"`
	Thresh string `json:"input_thresh"`
}

// MeasureLoudness runs loudnorm's analysis pass and returns the measured stats.
func (c *Client) MeasureLoudness(ctx context.Context, data []byte) (*LoudnessStats, error) {
	inputPath := path.Join(c.cfg.TmpDir, prefix+uuid.NewString())
	defer os.Remove(inputPath)

	if err := os.WriteFile(inputPath, data, 0644); err != nil {
		return nil, fmt.Errorf("write input file: %w", err)
	}

	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-i", inputPath,
		"-nostats",
		"-af", "loudnorm=I=-16:TP=-1.5:LRA=11:print_format=json",
		"-f", "null", "-",
	)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("failed to measure loudness: %w, stderr: %s", err, stderr.String())
	}

	// the JSON block is the tail of stderr, after the filter banner
	out := stderr.String()
	start := strings.LastIndex(out, "{")
	end := strings.LastIndex(out, "}")
	if start == -1 || end <= start {
		return nil, fmt.Errorf("no loudnorm stats in ffmpeg output")
	}

	var stats LoudnessStats
	if err := json.Unmarshal([]byte(out[start:end+1]), &stats); err != nil {
		return nil, fmt.Errorf("failed to parse loudnorm stats: %w", err)
	}

	// silence measures as -inf and would make the normalizing encode fail
	i, err := strconv.ParseFloat(stats.I, 64)
	if err != nil || math.IsInf(i, 0) || math.IsNaN(i) {
		return nil, fmt.Errorf("unmeasurable loudness: input_i=%q", stats.I)
	}

	return &stats, nil
}

// Ffmpeg2Mp3Normalized is Ffmpeg2Mp3 with a linear loudnorm pass driven by
// pre-measured stats, for normalizing streamed chunks without per-chunk gain
// jumps.
func (c *Client) Ffmpeg2Mp3Normalized(ctx context.Context, data []byte, stats *LoudnessStats) ([]byte, error) {
	inputPath := path.Join(c.cfg.TmpDir, prefix+uuid.NewString())
	outputPath := path.Join(c.cfg.TmpDir, prefix+uuid.NewString()+".mp3")

	defer os.Remove(inputPath)
	defer os.Remove(outputPath)

	if err := os.WriteFile(inputPath, data, 0644); err != nil {
		return nil, fmt.Errorf("write input file: %w", err)
	}

	loudnorm := fmt.Sprintf(
		"loudnorm=I=-16:TP=-1.5:LRA=11:measured_I=%s:measured_TP=%s:measured_LRA=%s:measured_thresh=%s:linear=true",
		stats.I, stats.TP, stats.LRA, stats.Thresh,
	)

	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-i", inputPath,
		"-nostats", "-loglevel", "0",
		"-af", loudnorm+",alimiter=limit=0.9:attack=5:release=50",
		"-ar", "44100", "-ac", "2", "-b:a", "192k", "-vn", "-f", "mp3",
		"-y",
		outputPath,
	)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("failed to encode normalized mp3: %w, stderr: %s", err, stderr.String())
	}

	output, err := os.ReadFile(outputPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read output file: %w", err)
	}

	return output, nil
}

func (c *Client) NormalizeAudio(ctx context.Context, data []byte) ([]byte, error) {
	inputPath := path.Join(c.cfg.TmpDir, prefix+uuid.NewString())
	outputPath := path.Join(c.cfg.TmpDir, prefix+uuid.NewString()+".mp3")

	defer os.Remove(inputPath)
	defer os.Remove(outputPath)

	if err := os.WriteFile(inputPath, data, 0644); err != nil {
		return nil, fmt.Errorf("write input file: %w", err)
	}

	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-i", inputPath,
		"-nostats", "-loglevel", "0",
		"-af", "loudnorm=I=-16:TP=-1.5:LRA=11",
		"-c:a", "mp3",
		"-b:a", "192k",
		"-ar", "44100",
		"-ac", "2",
		"-y",
		outputPath,
	)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("failed to normalize audio: %w, stderr: %s", err, stderr.String())
	}

	output, err := os.ReadFile(outputPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read normalized output: %w", err)
	}

	return output, nil
}

func (c *Client) TrimToWav(ctx context.Context, data []byte, maxDuration time.Duration) ([]byte, error) {
	inputPath := path.Join(c.cfg.TmpDir, prefix+uuid.NewString())
	outputPath := path.Join(c.cfg.TmpDir, prefix+uuid.NewString()+".wav")

	defer os.Remove(inputPath)
	defer os.Remove(outputPath)

	if err := os.WriteFile(inputPath, data, 0644); err != nil {
		return nil, fmt.Errorf("write input file: %w", err)
	}

	args := []string{"-i", inputPath}
	if maxDuration > 0 {
		args = append(args, "-t", fmt.Sprintf("%.3f", maxDuration.Seconds()))
	}
	args = append(args,
		"-ar", "44100",
		"-ac", "1",
		"-f", "wav",
		"-y",
		outputPath,
	)

	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("failed to run ffmpeg trim to wav: %w, stderr: %s", err, stderr.String())
	}

	output, err := os.ReadFile(outputPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read trimmed wav output: %w", err)
	}

	return output, nil
}
