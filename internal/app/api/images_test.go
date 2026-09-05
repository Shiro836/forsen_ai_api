package api

import (
	"bytes"
	"image"
	"image/color"
	"image/gif"
	"testing"
)

func encodeGIF(t *testing.T, frames int) []byte {
	t.Helper()
	g := &gif.GIF{}
	for f := 0; f < frames; f++ {
		g.Image = append(g.Image, image.NewPaletted(image.Rect(0, 0, 8, 8), color.Palette{color.Black, color.White}))
		g.Delay = append(g.Delay, 10)
	}
	var b bytes.Buffer
	if err := gif.EncodeAll(&b, g); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestAnimated(t *testing.T) {
	if animated(encodeGIF(t, 1), "gif") {
		t.Error("single-frame gif is not animated")
	}
	if !animated(encodeGIF(t, 3), "gif") {
		t.Error("multi-frame gif is animated")
	}

	vp8x := []byte("RIFF\x00\x00\x00\x00WEBPVP8X\x0a\x00\x00\x00")
	static := append(append([]byte{}, vp8x...), 0x10, 0, 0, 0, 0, 0, 0, 0)
	anim := append(append([]byte{}, vp8x...), 0x12, 0, 0, 0, 0, 0, 0, 0)
	if animated(static, "webp") || !animated(anim, "webp") {
		t.Error("webp animation flag misread")
	}
	if animated(anim, "png") {
		t.Error("png is never animated")
	}
}
