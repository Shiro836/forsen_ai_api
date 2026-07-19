package processor

import (
	"bytes"
	"image"
	"image/png"
	"testing"
)

func encodePNG(t *testing.T, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewNRGBA(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestDownscaleForLLM(t *testing.T) {
	small := encodePNG(t, 800, 600)
	got, err := downscaleForLLM(small)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !bytes.Equal(got, small) {
		t.Error("small image should pass through byte-identical")
	}

	big := encodePNG(t, 3840, 2160)
	got, err = downscaleForLLM(big)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(got))
	if err != nil {
		t.Fatalf("output not decodable: %v", err)
	}
	if cfg.Width != 1024 || cfg.Height != 576 {
		t.Errorf("expected 1024x576, got %dx%d", cfg.Width, cfg.Height)
	}

	if _, err := downscaleForLLM([]byte("not an image")); err == nil {
		t.Error("expected error for garbage input")
	}
}
