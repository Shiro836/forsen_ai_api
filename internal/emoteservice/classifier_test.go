package emoteservice

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"app/pkg/oai"
)

// PNG colour types from IHDR: 2 is truecolour, 6 adds an alpha channel.
const (
	pngTruecolor      = 2
	pngGrayscaleAlpha = 4
	pngTruecolorAlpha = 6
)

// pngColorType reads IHDR's colour-type byte, which sits at a fixed offset
// right after the signature and the header's length, tag, size and bit depth.
func pngColorType(t *testing.T, data []byte) byte {
	t.Helper()
	require.Greater(t, len(data), 25)
	return data[25]
}

// The model is free-text-prone, so anything outside the taxonomy is dropped
// rather than stored: a verdict must never hinge on an invented class.
func TestClassifyKeepsOnlyKnownClasses(t *testing.T) {
	c := &Classifier{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}

	got := c.keepKnownClasses("forsenCream", []string{"CUM", "nsfw", "cum", " hate ", "spoilers", ""})

	require.Equal(t, []string{ClassCum, ClassHate}, got)
}

func TestClassifyAcceptsAnEmptyClassList(t *testing.T) {
	c := &Classifier{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}

	require.Empty(t, c.keepKnownClasses("Prayge", nil))
	require.Empty(t, c.keepKnownClasses("Prayge", []string{}))
}

type stubVision struct{ reply string }

func (s stubVision) AskVisionJSON(context.Context, string, []oai.Image, json.RawMessage, float64) (string, error) {
	return s.reply, nil
}

// llama-server does not enforce json_object mode: both wrappers below were
// observed live, and each was failing every emote it happened to.
func TestClassifyExtractsTheJSONObject(t *testing.T) {
	for name, reply := range map[string]string{
		"fenced":             "```json\n{\"description\": \"a man doused in white cream\", \"classes\": [\"cum\"]}\n```",
		"preamble and fence": "Based on the visual evidence and the name:\n\n```json\n{\n\"description\": \"a man doused in white cream\",\n\"classes\": [\"cum\"]\n}\n```",
		"unfenced":           "{\"description\": \"a man doused in white cream\", \"classes\": [\"cum\"]}",
	} {
		t.Run(name, func(t *testing.T) {
			c := &Classifier{
				logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
				vision: stubVision{reply: reply},
			}

			got, err := c.Classify(context.Background(), "forsenCream", &Grid{PNG: []byte{1}, Tiles: 10})

			require.NoError(t, err)
			assert.Equal(t, "a man doused in white cream", got.Description)
			assert.Equal(t, []string{ClassCum}, got.Classes)
		})
	}
}

func testClassifier(t *testing.T) *Classifier {
	t.Helper()
	if _, err := exec.LookPath("magick"); err != nil {
		t.Skip("magick not in PATH")
	}
	cfg := &Config{TmpDir: t.TempDir()}
	cfg.withDefaults()
	return NewClassifier(slog.New(slog.NewTextHandler(io.Discard, nil)), nil, cfg)
}

// transparentWebp renders a webp with a transparent background, which is what
// every real emote looks like. Frames must differ or the encoder collapses them.
func transparentWebp(t *testing.T, frames int) []byte {
	t.Helper()
	dir := t.TempDir()

	args := make([]string, 0, frames+2)
	for i := 0; i < frames; i++ {
		frame := filepath.Join(dir, fmt.Sprintf("f%02d.png", i))
		msg, err := exec.Command("magick", "-size", "64x64", "xc:none",
			"-fill", fmt.Sprintf("rgb(%d,0,0)", i*20),
			"-draw", fmt.Sprintf("circle 32,32 32,%d", 10+i), frame).CombinedOutput()
		require.NoError(t, err, string(msg))
		args = append(args, frame)
	}

	path := filepath.Join(dir, "src.webp")
	args = append(args, "-loop", "0", path)
	msg, err := exec.Command("magick", args...).CombinedOutput()
	require.NoError(t, err, string(msg))

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return data
}

// An RGBA image hangs the embedding server and wedges its queue, so neither
// grid path may emit one.
func TestGridsCarryNoAlphaChannel(t *testing.T) {
	c := testClassifier(t)
	ctx := context.Background()

	source := transparentWebp(t, 12)
	require.Contains(t, []byte{pngTruecolorAlpha, pngGrayscaleAlpha}, sourceAlpha(t, source),
		"the fixture must carry alpha for this test to mean anything")

	grid, err := c.BuildGrid(ctx, source)
	require.NoError(t, err)
	assert.Equal(t, byte(pngTruecolor), pngColorType(t, grid.PNG), "montage grid must be flattened")
	assert.Equal(t, 12, grid.TotalFrames)

	static, err := c.BuildStatic(ctx, transparentWebp(t, 1))
	require.NoError(t, err)
	assert.Equal(t, byte(pngTruecolor), pngColorType(t, static.PNG), "static frame must be flattened")
	assert.Equal(t, 1, static.Tiles)
}

// sourceAlpha renders the fixture to PNG so its channel count can be read the
// same way as the pipeline's output.
func sourceAlpha(t *testing.T, webp []byte) byte {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "in.webp")
	out := filepath.Join(dir, "out.png")
	require.NoError(t, os.WriteFile(src, webp, 0o600))

	msg, err := exec.Command("magick", src+"[0]", out).CombinedOutput()
	require.NoError(t, err, string(msg))

	data, err := os.ReadFile(out)
	require.NoError(t, err)
	return pngColorType(t, data)
}

// scriptedVision answers each call in turn, which is what the two-pass fluids
// check needs: one reply for the taxonomy, one for the tears question.
type scriptedVision struct {
	replies []string
	calls   int
}

func (s *scriptedVision) AskVisionJSON(context.Context, string, []oai.Image, json.RawMessage, float64) (string, error) {
	reply := s.replies[min(s.calls, len(s.replies)-1)]
	s.calls++
	return reply, nil
}

// The second pass names the liquid; only a bodily one keeps the class. Each of
// these was a live failure that the class definition could not fix without
// breaking one of the others.
func TestClassifyKeepsFluidsForBodilyLiquids(t *testing.T) {
	for _, liquid := range []string{liquidSweat, liquidSaliva, liquidUrine, liquidVomit} {
		t.Run(liquid, func(t *testing.T) {
			vision := &scriptedVision{replies: []string{
				`{"description": "a droplet", "classes": ["fluids"]}`,
				`{"liquid": "` + liquid + `"}`,
			}}
			c := &Classifier{logger: slog.New(slog.NewTextHandler(io.Discard, nil)), vision: vision}

			got, err := c.Classify(context.Background(), "monkaSTEER", &Grid{PNG: []byte{1}, Tiles: 10})

			require.NoError(t, err)
			assert.Equal(t, []string{ClassFluids}, got.Classes)
			assert.Equal(t, 2, vision.calls)
		})
	}
}

func TestClassifyDropsFluidsForEverythingElse(t *testing.T) {
	for _, liquid := range []string{liquidTears, liquidBlood, liquidBeverage, liquidOther} {
		t.Run(liquid, func(t *testing.T) {
			vision := &scriptedVision{replies: []string{
				`{"description": "a liquid", "classes": ["fluids"]}`,
				`{"liquid": "` + liquid + `"}`,
			}}
			c := &Classifier{logger: slog.New(slog.NewTextHandler(io.Discard, nil)), vision: vision}

			got, err := c.Classify(context.Background(), "TeaTime", &Grid{PNG: []byte{1}, Tiles: 10})

			require.NoError(t, err)
			assert.Empty(t, got.Classes)
		})
	}
}

// Adjudicating the liquid must not disturb what else the emote was judged to be.
func TestClassifyKeepsOtherClassesWhenFluidsIsDropped(t *testing.T) {
	vision := &scriptedVision{replies: []string{
		`{"description": "crying while pulling a slot lever", "classes": ["fluids", "gambling"]}`,
		`{"liquid": "tears"}`,
	}}
	c := &Classifier{logger: slog.New(slog.NewTextHandler(io.Discard, nil)), vision: vision}

	got, err := c.Classify(context.Background(), "GAMBAADDICT", &Grid{PNG: []byte{1}, Tiles: 10})

	require.NoError(t, err)
	assert.Equal(t, []string{ClassGambling}, got.Classes)
}

// The second pass is only worth its call on an emote the first one flagged.
func TestClassifySkipsTheLiquidCheckWithoutFluids(t *testing.T) {
	vision := &scriptedVision{replies: []string{
		`{"description": "a cat bobbing its head", "classes": []}`,
	}}
	c := &Classifier{logger: slog.New(slog.NewTextHandler(io.Discard, nil)), vision: vision}

	_, err := c.Classify(context.Background(), "catJam", &Grid{PNG: []byte{1}, Tiles: 10})

	require.NoError(t, err)
	assert.Equal(t, 1, vision.calls)
}
