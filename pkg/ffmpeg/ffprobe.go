package ffmpeg

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path"
	"time"

	"github.com/google/uuid"
)

type FfprobeResult struct {
	Duration time.Duration
}

type ffprobeResult struct {
	Format struct {
		Duration string `json:"duration"`
	} `json:"format"`
}

func (c *Client) FfprobePath(ctx context.Context, path string) (*FfprobeResult, error) {
	cmd := exec.CommandContext(ctx, "ffprobe", "-v", "quiet", "-print_format", "json", "-show_format" /* "-show_streams", */, path)

	res, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("exec ffprobe: %w", err)
	}

	// fmt.Println(string(res))

	var result *ffprobeResult
	err = json.Unmarshal(res, &result)
	if err != nil {
		return nil, fmt.Errorf("unmarshal json: %w", err)
	}

	dur, err := time.ParseDuration(result.Format.Duration + "s")
	if err != nil {
		return nil, fmt.Errorf("parse duration: %w", err)
	}

	return &FfprobeResult{
		Duration: dur,
	}, nil
}

func (c *Client) Ffprobe(ctx context.Context, data []byte) (*FfprobeResult, error) {
	path := path.Join(c.cfg.TmpDir, prefix+uuid.NewString())

	err := os.WriteFile(path, data, 0644)
	if err != nil {
		return nil, fmt.Errorf("write file: %w", err)
	}

	defer os.Remove(path)

	return c.FfprobePath(ctx, path)
}
