package emoteservice

import (
	"image"
	"image/color"
	"math/rand"
	"testing"

	"github.com/stretchr/testify/require"
)

type imageFrames []image.Image

func (f imageFrames) Len() int                         { return len(f) }
func (f imageFrames) Frame(i int) (image.Image, error) { return f[i], nil }

func solid(size int, c color.NRGBA) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = c.R, c.G, c.B, c.A
	}
	return img
}

// darkBand blacks out one band of an opaque grey canvas. Alternating two bands
// of different widths is the sprite-motion case: a large part of the field
// brightens while a large part darkens, so the swing is big but not coherent.
func darkBand(size, from, to int) *image.NRGBA {
	img := solid(size, color.NRGBA{128, 128, 128, 255})
	for y := range size {
		for x := from; x < to; x++ {
			img.SetNRGBA(x, y, color.NRGBA{0, 0, 0, 255})
		}
	}
	return img
}

func repeat(frames []image.Image, times int) imageFrames {
	out := make(imageFrames, 0, len(frames)*times)
	for range times {
		out = append(out, frames...)
	}
	return out
}

func durations(ms, frames int) []int {
	out := make([]int, frames)
	for i := range out {
		out[i] = ms
	}
	return out
}

var (
	black = color.NRGBA{0, 0, 0, 255}
	white = color.NRGBA{255, 255, 255, 255}
	red   = color.NRGBA{255, 0, 0, 255}
)

func TestFlashStroberIsAHazard(t *testing.T) {
	frames := repeat([]image.Image{solid(64, black), solid(64, white)}, 5)

	m, err := MeasureFlash(frames, durations(100, frames.Len()))

	require.NoError(t, err)
	// A tenth of a second per frame is a 5 Hz square wave; the window edge lands
	// on a flash, which is worth a flash either way.
	require.InDelta(t, 5.0, m.Hz, 1)
	require.True(t, m.Flashing())
}

// Fast sprite motion is not flashing. The literal per-direction gate calls this
// a 5 Hz hazard, which is what would block popCat and a Pepe blinking.
func TestMovingSpriteIsNotAHazard(t *testing.T) {
	frames := repeat([]image.Image{darkBand(64, 0, 24), darkBand(64, 32, 64)}, 5)

	m, err := MeasureFlash(frames, durations(100, frames.Len()))

	require.NoError(t, err)
	require.Zero(t, m.Hz)
	require.False(t, m.Flashing())
	require.Greater(t, m.HzMax, 3.0)
}

// WCAG exempts fine balanced patterns, and TV-static emotes exist. Block
// reduction is what implements that exemption: per pixel this scores 19 Hz.
func TestFineNoiseIsExempt(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	frames := make(imageFrames, 12)
	for i := range frames {
		img := image.NewNRGBA(image.Rect(0, 0, 64, 64))
		for p := 0; p < len(img.Pix); p += 4 {
			v := uint8(0)
			if rng.Intn(2) == 1 {
				v = 255
			}
			img.Pix[p], img.Pix[p+1], img.Pix[p+2], img.Pix[p+3] = v, v, v, 255
		}
		frames[i] = img
	}

	m, err := MeasureFlash(frames, durations(40, len(frames)))

	require.NoError(t, err)
	require.Zero(t, m.Hz)
	require.Zero(t, m.HzMax)
}

func TestStaticImageMeasuresZero(t *testing.T) {
	m, err := MeasureFlash(imageFrames{solid(64, white)}, nil)

	require.NoError(t, err)
	require.Equal(t, FlashMeasure{}, m)
}

// A webp declaring 0 ms on every frame exists in the corpus. Unclamped its whole
// animation happens at once and any change in it reads as a hazard.
func TestZeroFrameDurationsAreClamped(t *testing.T) {
	frames := repeat([]image.Image{
		solid(64, black), solid(64, black), solid(64, black),
		solid(64, white), solid(64, white), solid(64, white),
	}, 2)

	m, err := MeasureFlash(frames, durations(0, frames.Len()))

	require.NoError(t, err)
	require.InDelta(t, 1.2, m.Duration, 1e-9)
	require.False(t, m.Flashing())
}

// A saturated red vibrating at a steady luminance is the case the general test
// cannot see; only the red transition catches it.
func TestRedFlashIsCounted(t *testing.T) {
	frames := repeat([]image.Image{solid(64, red), solid(64, black)}, 5)

	m, err := MeasureFlash(frames, durations(100, frames.Len()))

	require.NoError(t, err)
	require.Greater(t, m.RedHz, 3.0)
	require.True(t, m.Flashing())
}

// Durations are missing or disagree with the frame count often enough to matter;
// a nominal timeline beats a wrong one.
func TestMissingDurationsFallBackToAUniformRate(t *testing.T) {
	frames := repeat([]image.Image{solid(64, black), solid(64, white)}, 5)

	m, err := MeasureFlash(frames, []int{40, 40})

	require.NoError(t, err)
	require.InDelta(t, float64(len(frames))*0.1, m.Duration, 1e-9)
}

func le32(v uint32) []byte {
	return []byte{byte(v), byte(v >> 8), byte(v >> 16), byte(v >> 24)}
}

func riffChunk(fourcc string, payload []byte) []byte {
	out := append([]byte(fourcc), le32(uint32(len(payload)))...)
	out = append(out, payload...)
	if len(payload)&1 == 1 {
		out = append(out, 0)
	}
	return out
}

func animatedWebP(durationsMS ...int) []byte {
	body := riffChunk("VP8X", make([]byte, 10))
	// An odd-sized chunk before the frames: the walk must step over its pad byte.
	body = append(body, riffChunk("XMP ", []byte{1, 2, 3})...)
	for _, d := range durationsMS {
		payload := make([]byte, 16)
		payload[12], payload[13], payload[14] = byte(d), byte(d>>8), byte(d>>16)
		body = append(body, riffChunk("ANMF", payload)...)
	}

	out := append([]byte("RIFF"), le32(uint32(len(body)+4))...)
	out = append(out, "WEBP"...)
	return append(out, body...)
}

func TestFrameDurationsReadsANMFChunks(t *testing.T) {
	require.Equal(t, []int{42, 2300, 20}, frameDurations(animatedWebP(42, 2300, 20)))
}

func TestFrameDurationsIgnoresNonAnimations(t *testing.T) {
	require.Nil(t, frameDurations(animatedWebP()))
	require.Nil(t, frameDurations([]byte("RIFF")))
	require.Nil(t, frameDurations([]byte("not a webp at all")))
}
