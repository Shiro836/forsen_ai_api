package emoteservice

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"

	"app/pkg/oai"
)

// ClassifierVersion is the generation of the classification recipe. Bump it
// whenever the prompt, the class taxonomy or the grid recipe changes in a way
// that makes older answers incomparable — that is what marks stored rows stale
// and what /v1/reclassify selects on.
//
// A change that only narrows one class's definition is not one of those: it
// cannot add a class to a row that did not carry it, so it ships under the same
// version and takes a targeted resweep of the rows that do carry it
// (/v1/reclassify with a classes filter) rather than a full one. Bumps are for
// changes that can move any row — a new class, a broadened definition, a change
// to how the name is read. The corpus run is what tells the two apart: a
// narrowing that moves cases outside its own class was not a narrowing.
//
// Version 1 was the nsfw/innuendo booleans;
// 2 is the six content classes; 3 judges the name's slang meaning and motion
// innuendo alongside the pixels; 4 takes flashing away from the model, which
// was asked to infer a frequency from ten aliased samples and got every case
// wrong, and gives it to the photometric measure; 5 broadens violence to
// self-harm and dangerous acts, which the gore-only wording let through, and
// adds the gambling and fluids classes.
const ClassifierVersion = 5

// VisionModel is the multimodal completion the classifier judges grids with.
type VisionModel interface {
	AskVisionJSON(ctx context.Context, prompt string, images []oai.Image, schema json.RawMessage, temperature float64) (string, error)
}

// classifySchema constrains the reply. json_object mode is accepted and ignored
// by llama-server, so the fenced and prefaced answers that used to fail whole
// classes of emote were never actually excluded; a schema compiles to a grammar
// that cannot produce them.
var classifySchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"description": {"type": "string"},
		"classes": {"type": "array", "items": {"type": "string"}}
	},
	"required": ["description", "classes"],
	"additionalProperties": false
}`)

// Classification is the vision model's verdict on one emote.
type Classification struct {
	Description string   `json:"description"`
	Classes     []string `json:"classes"`
}

type Classifier struct {
	logger  *slog.Logger
	vision  VisionModel
	model   string
	magick  string
	tmpDir  string
	frames  int
	columns int
}

func NewClassifier(logger *slog.Logger, vision VisionModel, cfg *Config) *Classifier {
	return &Classifier{
		logger:  logger,
		vision:  vision,
		model:   cfg.Vision.Model,
		magick:  cfg.MagickBin,
		tmpDir:  cfg.TmpDir,
		frames:  cfg.GridFrames,
		columns: cfg.GridColumns,
	}
}

func (c *Classifier) Model() string { return c.model }

// Grid is what the vision model is shown: a montage of sampled frames for an
// animated emote, or a single frame for a static one. Flash is measured from the
// same coalesced frames the montage samples, so it costs no second decode.
type Grid struct {
	PNG         []byte
	Tiles       int
	TotalFrames int
	Flash       FlashMeasure
}

// BuildStatic converts a single-frame emote to PNG. There is nothing to sample,
// so it skips the montage entirely.
func (c *Classifier) BuildStatic(ctx context.Context, webp []byte) (*Grid, error) {
	dir, src, err := c.stage(webp)
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)

	out := filepath.Join(dir, "frame.png")
	if msg, err := c.run(ctx, c.magick, append([]string{src, "-coalesce"}, flattenArgs(out)...)...); err != nil {
		return nil, fmt.Errorf("convert static emote: %w (%s)", err, msg)
	}

	data, err := os.ReadFile(out)
	if err != nil {
		return nil, fmt.Errorf("read static frame: %w", err)
	}
	return &Grid{PNG: data, Tiles: 1, TotalFrames: 1}, nil
}

// BuildGrid renders an animated webp as a single labelled montage of
// evenly-sampled frames, and scores the whole animation for flashing along the
// way.
func (c *Classifier) BuildGrid(ctx context.Context, webp []byte) (*Grid, error) {
	dir, src, err := c.stage(webp)
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)

	frames, err := c.coalesce(ctx, dir, src)
	if err != nil {
		return nil, err
	}

	flash, err := MeasureFlash(pngFrames(frames), c.durations(webp, len(frames)))
	if err != nil {
		return nil, fmt.Errorf("measure flashing: %w", err)
	}

	sampled := sampleIndices(len(frames), c.frames)
	args := []string{"montage"}
	for _, idx := range sampled {
		args = append(args, "-label", strconv.Itoa(idx), frames[idx])
	}
	tiled := filepath.Join(dir, "tiled.png")
	args = append(args,
		"-tile", fmt.Sprintf("%dx", c.columns),
		"-geometry", "+2+2",
		"-background", "white",
		"-pointsize", "14",
		tiled)

	if out, err := c.run(ctx, c.magick, args...); err != nil {
		return nil, fmt.Errorf("montage frames: %w (%s)", err, out)
	}

	// montage keeps an alpha channel whatever -background says, and ignores the
	// alpha operators when they are passed to it, so flattening needs its own pass.
	grid := filepath.Join(dir, "grid.png")
	if out, err := c.run(ctx, c.magick, append([]string{tiled}, flattenArgs(grid)...)...); err != nil {
		return nil, fmt.Errorf("flatten grid: %w (%s)", err, out)
	}

	data, err := os.ReadFile(grid)
	if err != nil {
		return nil, fmt.Errorf("read grid: %w", err)
	}
	return &Grid{PNG: data, Tiles: len(sampled), TotalFrames: len(frames), Flash: flash}, nil
}

// MeasureWebP scores an emote for photosensitive flashing without building a
// grid or calling the vision model, which is what lets the backfill rescore the
// whole registry on CPU alone.
func (c *Classifier) MeasureWebP(ctx context.Context, webp []byte) (FlashMeasure, error) {
	if len(frameDurations(webp)) < 2 {
		return FlashMeasure{}, nil
	}

	dir, src, err := c.stage(webp)
	if err != nil {
		return FlashMeasure{}, err
	}
	defer os.RemoveAll(dir)

	frames, err := c.coalesce(ctx, dir, src)
	if err != nil {
		return FlashMeasure{}, err
	}
	return MeasureFlash(pngFrames(frames), c.durations(webp, len(frames)))
}

// durations reads the animation's real per-frame timing. The two counts have
// never been observed to disagree, so one that does is worth hearing about: the
// measure falls back to a uniform rate and any Hz it reports is nominal.
func (c *Classifier) durations(webp []byte, frames int) []int {
	durations := frameDurations(webp)
	if len(durations) != frames {
		c.logger.Warn("webp frame durations do not match the coalesced frames, timing the animation uniformly",
			"anmf_chunks", len(durations), "frames", frames)
	}
	return durations
}

// coalesce expands an animation into whole frames. 7TV webps store deltas, and
// ImageMagick's scene-selection syntax hands back those partial frames verbatim.
func (c *Classifier) coalesce(ctx context.Context, dir, src string) ([]string, error) {
	if out, err := c.run(ctx, c.magick, src, "-coalesce", filepath.Join(dir, "frame_%04d.png")); err != nil {
		return nil, fmt.Errorf("coalesce frames: %w (%s)", err, out)
	}

	frames, err := filepath.Glob(filepath.Join(dir, "frame_*.png"))
	if err != nil {
		return nil, fmt.Errorf("list frames: %w", err)
	}
	if len(frames) == 0 {
		return nil, fmt.Errorf("coalesce produced no frames")
	}
	sort.Strings(frames)
	return frames, nil
}

// flattenArgs composites onto white and writes plain 8-bit RGB. Emotes are
// transparent, and an RGBA image sent to the embedding server hangs the request
// and wedges its queue, so nothing may leave this package with an alpha channel.
// PNG24 is what pins that: without it ImageMagick picks the encoding, and a
// low-colour frame comes back as a palette image instead.
func flattenArgs(out string) []string {
	return []string{"-background", "white", "-alpha", "remove", "-alpha", "off", "PNG24:" + out}
}

// stage writes the source image into a fresh temp dir; the caller removes it.
func (c *Classifier) stage(webp []byte) (dir, src string, err error) {
	dir, err = os.MkdirTemp(c.tmpDir, "emote-grid-")
	if err != nil {
		return "", "", fmt.Errorf("create grid tmp dir: %w", err)
	}
	src = filepath.Join(dir, "src.webp")
	if err := os.WriteFile(src, webp, 0o600); err != nil {
		os.RemoveAll(dir)
		return "", "", fmt.Errorf("write source webp: %w", err)
	}
	return dir, src, nil
}

func (c *Classifier) run(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// sampleIndices picks up to want frame indices spread evenly over total.
func sampleIndices(total, want int) []int {
	if want <= 0 || total <= want {
		out := make([]int, total)
		for i := range out {
			out[i] = i
		}
		return out
	}
	out := make([]int, want)
	for i := range out {
		out[i] = i * total / want
	}
	return out
}

const classifyPromptHeaderAnimated = `The image is a grid of %d frames sampled evenly from an animated Twitch/7TV emote named "%s", ordered left to right, top to bottom and labelled with their frame index.`

const classifyPromptHeaderStatic = `The image is a Twitch/7TV emote named "%s".`

const classifyPromptBody = `

Judge the emote as it would appear next to a message on a livestream.

Judge the name together with the pixels. Emote names often carry internet or Twitch slang whose meaning the frames do not show, and when you recognise the name as a term for something, that meaning counts as content. Never invent a meaning for a name you do not already know: Twitch names are routinely an ordinary word plus a meaningless suffix such as -ge, -W or -ega, and splitting an unfamiliar name into invented parts is not recognition — such a name carries no signal at all. In emote names the prefixes "pp" and "peepo" mean the small Pepe the Frog character, never anatomy. Judge the animation as a whole too, since a motion can depict something no single frame does.

Reply with a JSON object and nothing else:
{"description": "one or two sentences describing what the emote shows", "classes": ["zero or more of the class names below"]}

Use only these class names, and include one only when it clearly applies:
"cum" - white or creamy liquid innuendo, including the milk, cream and yogurt trope; treat those as the same thing.
"sexual" - explicit sexual content, nudity, or unmistakable sexual innuendo: a name that plainly refers to a sex act, to porn or to arousal, or a motion that plainly mimes a sex act. Dancing, head-bobbing, laughing, crying and excitement are not sexual.
"violence" - gore, graphic violence, a weapon aimed at someone or at the viewer, or self-harm and dangerous acts shown for real: setting oneself on fire, cutting, choking, turning a weapon on oneself. Slapstick cartoon impacts are not violence: bonks, hammer hits, slaps, punches and falling anvils are ordinary Twitch vocabulary.
"hate" - slurs, hate symbols or racist caricatures.
"drugs" - drug use or drug paraphernalia.
"gambling" - slot machines, casino games, betting, or a jackpot being celebrated. The imagery has to be there: a name that merely alludes to gambling, and joking about losing money, are not this class.
"fluids" - any depiction of a bodily fluid or its release, stylized cartoon drops included: urinating, defecating, vomiting, spitting, drooling, a tongue-out lick, or sweat down to a single bead on a nervous face; a drawn droplet counts whether it reads as sweat or as a tear. The name alone is never enough - an angry face called "PISSED" shows no fluid. White or creamy liquid is "cum", and blood is "violence".

Use an empty list when none of them apply.`

// The liquid the second pass may name. Only the bodily ones keep the class:
// tears are excluded by product decision, blood belongs to violence, and a drink
// belongs to nobody.
const (
	liquidSweat    = "sweat"
	liquidTears    = "tears"
	liquidSaliva   = "saliva"
	liquidUrine    = "urine"
	liquidVomit    = "vomit"
	liquidBlood    = "blood"
	liquidBeverage = "beverage_or_food"
	liquidOther    = "other_nonbodily"
)

var bodilyLiquids = []string{liquidSweat, liquidSaliva, liquidUrine, liquidVomit}

// liquidPromptBody re-asks about one emote the fluids class caught, because the
// class definition cannot carry the distinction itself. Every attempt to put it
// there traded one case for another — a rule broad enough to catch a stylized
// sweat bead also catches a teapot's arc of tea, and narrowing it until the
// teapot escaped stopped the model from mentioning the sweat at all. So the
// definition stays broad and only detects that liquid is present; naming the
// liquid is one focused question over imagery already known to contain some.
const liquidPromptBody = `

This emote has been judged to show a liquid or a droplet. Answer one question about it.

What is the liquid or droplet shown? Answer with the single best option:
"sweat" - sweat running off a body, or a stylized bead on or beside a nervous, straining or embarrassed face
"tears" - tears welling in or falling from the eyes of a sad, crying or distressed face
"saliva" - spit, drool, or a tongue-out lick
"urine" - urine, shown or plainly implied
"vomit" - vomit
"blood" - blood
"beverage_or_food" - tea, coffee, water, beer, soup or any other drink or food liquid, however it pours or splashes
"other_nonbodily" - rain, paint, slime, a potion, or any other liquid that comes out of no body

If more than one is shown, answer with the one the emote is about.

Reply with a JSON object and nothing else:
{"liquid": "<one of the options above>"}`

var liquidSchema = json.RawMessage(`{
	"type": "object",
	"properties": {
		"liquid": {
			"type": "string",
			"enum": ["sweat", "tears", "saliva", "urine", "vomit", "blood", "beverage_or_food", "other_nonbodily"]
		}
	},
	"required": ["liquid"],
	"additionalProperties": false
}`)

// liquidKind names what the first pass saw. It costs one extra vision call on
// the few percent of emotes the fluids class catches, and nothing on the rest.
func (c *Classifier) liquidKind(ctx context.Context, header, name string, grid *Grid) (string, error) {
	raw, err := c.vision.AskVisionJSON(ctx, header+liquidPromptBody,
		[]oai.Image{{MIME: "image/png", Data: grid.PNG}}, liquidSchema, 0)
	if err != nil {
		return "", fmt.Errorf("liquid check %s: %w", name, err)
	}

	var out struct {
		Liquid string `json:"liquid"`
	}
	if err := json.Unmarshal([]byte(extractJSONObject(raw)), &out); err != nil {
		return "", fmt.Errorf("liquid check %s: parse response %q: %w", name, raw, err)
	}
	return out.Liquid, nil
}

// header names what the model is looking at: a montage of sampled frames, or a
// single image for a static emote.
func (c *Classifier) header(name string, grid *Grid) string {
	if grid.Tiles <= 1 {
		return fmt.Sprintf(classifyPromptHeaderStatic, name)
	}
	return fmt.Sprintf(classifyPromptHeaderAnimated, grid.Tiles, name)
}

// Classify asks the vision model to judge a grid. The emote name is given as a
// text signal because it often carries meaning the pixels do not.
func (c *Classifier) Classify(ctx context.Context, name string, grid *Grid) (*Classification, error) {
	header := c.header(name, grid)
	prompt := header + classifyPromptBody

	raw, err := c.vision.AskVisionJSON(ctx, prompt, []oai.Image{{MIME: "image/png", Data: grid.PNG}}, classifySchema, 0)
	if err != nil {
		return nil, fmt.Errorf("classify %s: %w", name, err)
	}

	var out Classification
	if err := json.Unmarshal([]byte(extractJSONObject(raw)), &out); err != nil {
		return nil, fmt.Errorf("classify %s: parse response %q: %w", name, raw, err)
	}

	out.Classes = c.keepKnownClasses(name, out.Classes)

	if slices.Contains(out.Classes, ClassFluids) {
		liquid, err := c.liquidKind(ctx, header, name, grid)
		if err != nil {
			return nil, err
		}
		if !slices.Contains(bodilyLiquids, liquid) {
			out.Classes = slices.DeleteFunc(out.Classes, func(class string) bool { return class == ClassFluids })
		}
		c.logger.Info("adjudicated fluid imagery",
			"emote", name, "liquid", liquid, "kept", slices.Contains(out.Classes, ClassFluids))
	}

	return &out, nil
}

// extractJSONObject takes the outermost { ... } from a reply. llama-server does
// not enforce json_object mode, and the model wraps its answer in a ```json
// fence or a sentence of preamble often enough to fail whole classes of emote.
// A reply with no object is returned as-is so the parse error carries it.
func extractJSONObject(raw string) string {
	start := strings.IndexByte(raw, '{')
	end := strings.LastIndexByte(raw, '}')
	if start < 0 || end < start {
		return strings.TrimSpace(raw)
	}
	return raw[start : end+1]
}

// keepKnownClasses drops anything outside the taxonomy. A model that invents a
// class must not create a verdict nobody can configure, and the warning is how
// a drifting prompt gets noticed. Flashing goes with it whatever the model says:
// the photometric measure owns that class in both directions.
func (c *Classifier) keepKnownClasses(name string, raw []string) []string {
	kept := slices.DeleteFunc(FilterContentClasses(raw), func(class string) bool {
		return class == ClassFlashing
	})

	for _, class := range raw {
		if !IsContentClass(strings.ToLower(strings.TrimSpace(class))) {
			c.logger.Warn("vision model returned an unknown content class",
				"emote", name, "class", class, "kept", kept)
		}
	}

	return kept
}
