package processor

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/png"

	"github.com/disintegration/imaging"
)

// llmImageMaxDim caps the resolution of images attached to LLM requests.
// Stored images can be up to 4K for share links, but the vision model reads
// them at 1024: a 4K image costs ~4x the prompt tokens (measured 4102 vs
// 1054 on qwen36-hauhau) against 16k context per slot.
const llmImageMaxDim = 1024

type stillRenderer interface {
	StillPNG(ctx context.Context, data []byte, maxDim int) ([]byte, error)
}

// imageForLLM returns a PNG no larger than llmImageMaxDim. PNGs already
// within the limit pass through byte-identical; anything Go cannot decode
// (animated webp) goes through ffmpeg for its first frame.
func imageForLLM(ctx context.Context, ff stillRenderer, data []byte) ([]byte, error) {
	out, err := fitPNG(data)
	if err != nil && ff != nil {
		return ff.StillPNG(ctx, data, llmImageMaxDim)
	}
	return out, err
}

func fitPNG(data []byte) ([]byte, error) {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("decode config: %w", err)
	}
	if format == "png" && cfg.Width <= llmImageMaxDim && cfg.Height <= llmImageMaxDim {
		return data, nil
	}

	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}

	dst := imaging.Fit(src, llmImageMaxDim, llmImageMaxDim, imaging.Lanczos)

	var out bytes.Buffer
	if err := png.Encode(&out, dst); err != nil {
		return nil, fmt.Errorf("encode: %w", err)
	}

	return out.Bytes(), nil
}
