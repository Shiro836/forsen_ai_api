package processor

import (
	"app/pkg/ffmpeg"
	"bytes"
	"context"
	"image"
	"image/color"
	"image/gif"
	"image/png"
	"os/exec"
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

func encodeGIF(t *testing.T, frames int) []byte {
	t.Helper()
	g := &gif.GIF{}
	for f := 0; f < frames; f++ {
		frame := image.NewPaletted(image.Rect(0, 0, 16, 16), color.Palette{color.Black, color.White})
		for i := range frame.Pix {
			frame.Pix[i] = byte((i + f) % 2)
		}
		g.Image = append(g.Image, frame)
		g.Delay = append(g.Delay, 10)
	}
	var buf bytes.Buffer
	if err := gif.EncodeAll(&buf, g); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

type fakeStill struct{ calls int }

func (f *fakeStill) StillPNG(_ context.Context, _ []byte, _ int) ([]byte, error) {
	f.calls++
	return []byte("png-from-ffmpeg"), nil
}

func TestImageForLLM(t *testing.T) {
	ctx := context.Background()

	small := encodePNG(t, 800, 600)
	got, err := imageForLLM(ctx, nil, small)
	if err != nil || !bytes.Equal(got, small) {
		t.Fatalf("small png should pass through byte-identical: err=%v", err)
	}

	big := encodePNG(t, 3840, 2160)
	got, err = imageForLLM(ctx, nil, big)
	if err != nil {
		t.Fatal(err)
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(got))
	if err != nil || format != "png" || cfg.Width != 1024 || cfg.Height != 576 {
		t.Fatalf("expected 1024x576 png, got %s %dx%d err=%v", format, cfg.Width, cfg.Height, err)
	}

	got, err = imageForLLM(ctx, nil, encodeGIF(t, 1))
	if err != nil {
		t.Fatal(err)
	}
	if _, format, _ := image.DecodeConfig(bytes.NewReader(got)); format != "png" {
		t.Fatalf("gif must be re-encoded as png, got %s", format)
	}

	if _, err := exec.LookPath("ffmpeg"); err == nil {
		animWebP, err := ffmpeg.New(&ffmpeg.Config{}).FitWebP(ctx, encodeGIF(t, 2), 64, 64, 80)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := image.Decode(bytes.NewReader(animWebP)); err == nil {
			t.Fatal("fixture must be undecodable by Go for this case to mean anything")
		}
		still := &fakeStill{}
		got, err = imageForLLM(ctx, still, animWebP)
		if err != nil || string(got) != "png-from-ffmpeg" || still.calls != 1 {
			t.Fatalf("animated webp should fall through to ffmpeg: %q err=%v calls=%d", got, err, still.calls)
		}
	}

	if _, err := imageForLLM(ctx, nil, []byte("not an image")); err == nil {
		t.Error("expected error for garbage input")
	}
}
