package ffmpeg

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path"
	"time"

	"github.com/google/uuid"
)

// for the purpose of compressing audio to the highest degree
func (c *Client) Ffmpeg2Mp3Path(ctx context.Context, inputPath string) ([]byte, error) {
	outputPath := path.Join(c.cfg.TmpDir, prefix+uuid.NewString())

	defer os.Remove(outputPath)

	cmd := exec.CommandContext(ctx, "ffmpeg", "-i", inputPath, "-nostats", "-loglevel", "0", "-ar", "44100", "-ac", "2", "-b:a", "192k", "-vn", "-f", "mp3", outputPath)

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("failed to run ffmpeg: %w", err)
	}

	output, err := os.ReadFile(outputPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read output file: %w", err)
	}

	return output, nil
}

func (c *Client) Ffmpeg2Mp3(ctx context.Context, data []byte) ([]byte, error) {
	path := path.Join(c.cfg.TmpDir, prefix+uuid.NewString())

	err := os.WriteFile(path, data, 0644)
	if err != nil {
		return nil, fmt.Errorf("write file: %w", err)
	}

	defer os.Remove(path)

	return c.Ffmpeg2Mp3Path(ctx, path)
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

	// Create temporary files for each audio input
	tempFiles := make([]string, len(audioFiles))
	defer func() {
		// Clean up temporary files
		for _, tempFile := range tempFiles {
			if tempFile != "" {
				os.Remove(tempFile)
			}
		}
	}()

	// Write each audio file to a temporary file
	for i, audioData := range audioFiles {
		tempFile := path.Join(c.cfg.TmpDir, prefix+uuid.NewString())
		err := os.WriteFile(tempFile, audioData, 0644)
		if err != nil {
			return nil, fmt.Errorf("write temp file %d: %w", i, err)
		}
		tempFiles[i] = tempFile
	}

	// Create output file path with .mp3 extension
	outputPath := path.Join(c.cfg.TmpDir, prefix+uuid.NewString()+".mp3")
	defer os.Remove(outputPath)

	// Build ffmpeg command for concatenation
	// Using the concat filter for better compatibility
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
