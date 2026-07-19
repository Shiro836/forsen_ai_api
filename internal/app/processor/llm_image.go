package processor

import (
	"bytes"
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

// downscaleForLLM re-encodes an image if it exceeds llmImageMaxDim in either
// dimension; smaller images pass through byte-identical.
func downscaleForLLM(data []byte) ([]byte, error) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("decode config: %w", err)
	}
	if cfg.Width <= llmImageMaxDim && cfg.Height <= llmImageMaxDim {
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
