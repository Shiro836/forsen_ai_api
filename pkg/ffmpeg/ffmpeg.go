package ffmpeg

import (
	"context"
	"os/exec"
)

// for the purpose of compressing audio to the highest degree
func (c *Client) FfmpegWav2Mp3Path(ctx context.Context, path string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "ffmpeg", "-i", path, "-vn", "-ar", "44100", "-ac", "2", "-ab", "192k", "-f", "mp3", "-")
	// TODO: do this shit
	return cmd.CombinedOutput()
}
