package ffmpeg

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path"

	"github.com/google/uuid"
)

// for the purpose of compressing audio to the highest degree
func (c *Client) Ffmpeg2Mp3Path(ctx context.Context, inputPath string) ([]byte, error) {
	outputPath := path.Join(c.cfg.TmpDir, prefix+uuid.NewString())

	defer os.Remove(outputPath)

	cmd := exec.CommandContext(ctx, "ffmpeg", "-i", inputPath, "-nostats", "-loglevel", "0", "-ar", "44100", "-ac", "1", "-b:a", "192k", "-vn", "-f", "mp3", outputPath)

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
