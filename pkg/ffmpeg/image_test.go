package ffmpeg

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/gif"
	"image/png"
	"os/exec"
	"testing"
)

func testPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := range img.Pix {
		img.Pix[i] = byte(i)
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func testGIF(t *testing.T, frames int) []byte {
	t.Helper()
	g := &gif.GIF{LoopCount: 0}
	for f := 0; f < frames; f++ {
		pal := color.Palette{color.Black, color.RGBA{R: byte(80 * f), G: 0, B: 200, A: 255}}
		frame := image.NewPaletted(image.Rect(0, 0, 400, 300), pal)
		for i := range frame.Pix {
			frame.Pix[i] = byte((i / 7) % 2)
		}
		g.Image = append(g.Image, frame)
		g.Delay = append(g.Delay, 10)
	}
	var b bytes.Buffer
	if err := gif.EncodeAll(&b, g); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func isWebP(b []byte) bool {
	return len(b) > 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP"
}

func requireFfmpeg(t *testing.T) *Client {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not in PATH")
	}
	return New(&Config{})
}

func TestFitWebP(t *testing.T) {
	c := requireFfmpeg(t)
	ctx := context.Background()

	t.Run("static png scales down", func(t *testing.T) {
		src := testPNG(t, 800, 400)
		out, err := c.FitWebP(ctx, src, 200, 200, 80)
		if err != nil {
			t.Fatal(err)
		}
		if !isWebP(out) || bytes.Contains(out, []byte("ANIM")) || len(out) >= len(src) {
			t.Fatalf("expected a smaller static webp: len=%d animated=%v", len(out), bytes.Contains(out, []byte("ANIM")))
		}
	})

	t.Run("never upscales", func(t *testing.T) {
		out, err := c.FitWebP(ctx, testPNG(t, 120, 80), 3840, 3840, 80)
		if err != nil {
			t.Fatal(err)
		}
		w, h, err := c.ImageDims(ctx, out)
		if err != nil || w != 120 || h != 80 {
			t.Fatalf("got %dx%d err=%v", w, h, err)
		}
	})

	t.Run("height bound box", func(t *testing.T) {
		out, err := c.FitWebP(ctx, testPNG(t, 800, 400), 1200, 200, 80)
		if err != nil {
			t.Fatal(err)
		}
		w, h, err := c.ImageDims(ctx, out)
		if err != nil || w != 400 || h != 200 {
			t.Fatalf("got %dx%d err=%v", w, h, err)
		}
	})

	t.Run("animated gif keeps frames", func(t *testing.T) {
		out, err := c.FitWebP(ctx, testGIF(t, 3), 200, 200, 80)
		if err != nil {
			t.Fatal(err)
		}
		if !isWebP(out) || bytes.Count(out, []byte("ANMF")) != 3 {
			t.Fatalf("expected 3 ANMF frames, got %d", bytes.Count(out, []byte("ANMF")))
		}
	})

	t.Run("garbage input fails", func(t *testing.T) {
		if _, err := c.FitWebP(ctx, []byte("not an image"), 200, 200, 80); err == nil {
			t.Fatal("expected an error")
		}
	})
}

func TestStillPNG(t *testing.T) {
	c := requireFfmpeg(t)
	out, err := c.StillPNG(context.Background(), testGIF(t, 3), 100)
	if err != nil {
		t.Fatal(err)
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(out))
	if err != nil || format != "png" || cfg.Width != 100 || cfg.Height != 75 {
		t.Fatalf("got %s %dx%d err=%v", format, cfg.Width, cfg.Height, err)
	}
}

func TestImageDims(t *testing.T) {
	c := requireFfmpeg(t)
	w, h, err := c.ImageDims(context.Background(), testGIF(t, 2))
	if err != nil || w != 400 || h != 300 {
		t.Fatalf("got %dx%d err=%v", w, h, err)
	}
	if _, _, err := c.ImageDims(context.Background(), []byte("nope")); err == nil {
		t.Fatal("expected an error")
	}
}
