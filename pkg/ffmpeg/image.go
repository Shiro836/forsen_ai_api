package ffmpeg

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// FitWebP scales an image down to fit within maxW x maxH (never up) and
// encodes it as WebP at the given quality (0-100). Animated inputs keep every
// frame and its timing.
func (c *Client) FitWebP(ctx context.Context, data []byte, maxW, maxH, quality int) ([]byte, error) {
	return runImage(ctx, data,
		"-vf", fitFilter(maxW, maxH),
		"-c:v", "libwebp_anim",
		"-quality", strconv.Itoa(quality),
		"-loop", "0",
		"-f", "webp", "pipe:1",
	)
}

// StillPNG returns the first frame scaled down to fit within maxDim as PNG,
// for consumers that cannot read animated formats.
func (c *Client) StillPNG(ctx context.Context, data []byte, maxDim int) ([]byte, error) {
	return runImage(ctx, data,
		"-frames:v", "1",
		"-vf", fitFilter(maxDim, maxDim),
		"-c:v", "png",
		"-f", "image2pipe", "pipe:1",
	)
}

// ImageDims returns the pixel size of an image via ffprobe.
func (c *Client) ImageDims(ctx context.Context, data []byte) (width, height int, err error) {
	cmd := exec.CommandContext(ctx, "ffprobe",
		"-v", "error",
		"-select_streams", "v:0",
		"-show_entries", "stream=width,height",
		"-of", "json",
		"pipe:0",
	)
	cmd.Stdin = bytes.NewReader(data)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return 0, 0, fmt.Errorf("ffprobe: %w\nffprobe output:\n%s", err, strings.TrimSpace(stderr.String()))
	}
	var out struct {
		Streams []struct {
			Width  int `json:"width"`
			Height int `json:"height"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		return 0, 0, fmt.Errorf("parse ffprobe output: %w", err)
	}
	if len(out.Streams) == 0 || out.Streams[0].Width <= 0 || out.Streams[0].Height <= 0 {
		return 0, 0, fmt.Errorf("ffprobe: no video stream")
	}
	return out.Streams[0].Width, out.Streams[0].Height, nil
}

func fitFilter(maxW, maxH int) string {
	return "scale=w='min(iw," + strconv.Itoa(maxW) + ")':h='min(ih," + strconv.Itoa(maxH) + ")':force_original_aspect_ratio=decrease:flags=lanczos"
}

func runImage(ctx context.Context, data []byte, outputArgs ...string) ([]byte, error) {
	args := append([]string{"-nostats", "-loglevel", "error", "-i", "pipe:0"}, outputArgs...)
	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	cmd.Stdin = bytes.NewReader(data)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg image: %w\nffmpeg output:\n%s", err, strings.TrimSpace(stderr.String()))
	}
	if stdout.Len() == 0 {
		return nil, fmt.Errorf("ffmpeg image: empty output\nffmpeg output:\n%s", strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}
