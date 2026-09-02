package emoteservice

import (
	"strings"
	"time"

	"app/pkg/infinity"
	"app/pkg/llm"
	"app/pkg/s3client"
	"app/pkg/seventv"
)

type Config struct {
	Host string `yaml:"host"`
	Port int    `yaml:"port"`
	// SharedSecret guards every route but /health. Callers send it as
	// X-Emote-Token. Real per-user auth arrives with the moderation UI.
	SharedSecret string `yaml:"shared_secret"`

	ConnStr string `yaml:"conn_str"`

	// Vision is the classifier endpoint. It is the same llama-server the
	// characters run on, which is why the worker gates on its idleness.
	Vision llm.Config `yaml:"vision"`
	// VisionSlotsURL is llama-server's /slots endpoint, which the worker polls to
	// dispatch a classification only while the character model is idle. It is
	// derived from Vision.URL when unset; /slots sits at the server root, beside
	// /v1 rather than under it.
	VisionSlotsURL string          `yaml:"vision_slots_url"`
	Infinity       infinity.Config `yaml:"infinity"`
	SevenTV        seventv.Config  `yaml:"seventv"`
	S3             s3client.Config `yaml:"s3"`

	// ClassifyInterval is the fallback pace, used only while llama-server's
	// /slots cannot be read. Idleness gating replaced it as the normal mechanism:
	// a fixed interval both wastes an idle GPU and still collides with a busy one.
	ClassifyInterval time.Duration `yaml:"classify_interval"`
	MaxAttempts      int           `yaml:"max_attempts"`

	// SeedCount is how deep the global popularity crawl goes when a seed request
	// does not say. 7TV paginates at most seventv.MaxTopEmotes.
	SeedCount int `yaml:"seed_count"`
	// SeedQueries seed named slices of the catalogue alongside the global top-N.
	// The global head alone was measured to cover well under half of what a real
	// channel's viewers type, and a channel's own emotes cluster under its name.
	SeedQueries []SeedSource `yaml:"seed_queries"`

	GridFrames  int    `yaml:"grid_frames"`
	GridColumns int    `yaml:"grid_columns"`
	MagickBin   string `yaml:"magick_bin"`
	TmpDir      string `yaml:"tmp_dir"`

	// ImageThreshold and DescThreshold are cosine cutoffs for rule preview. They
	// only ever produce candidates a human confirms, so they are tuned loose.
	ImageThreshold float64 `yaml:"image_threshold"`
	DescThreshold  float64 `yaml:"desc_threshold"`
	RuleCandidates int     `yaml:"rule_candidates"`
}

func (c *Config) withDefaults() {
	if c.Port == 0 {
		c.Port = 8086
	}
	if c.ClassifyInterval <= 0 {
		c.ClassifyInterval = 10 * time.Second
	}
	if c.MaxAttempts <= 0 {
		c.MaxAttempts = 3
	}
	if c.GridFrames <= 0 {
		c.GridFrames = 10
	}
	if c.GridColumns <= 0 {
		c.GridColumns = 5
	}
	if c.MagickBin == "" {
		c.MagickBin = "magick"
	}
	if c.ImageThreshold <= 0 {
		c.ImageThreshold = 0.2
	}
	if c.DescThreshold <= 0 {
		c.DescThreshold = 0.2
	}
	if c.RuleCandidates <= 0 {
		c.RuleCandidates = 200
	}
	if c.Vision.MaxTokens <= 0 {
		c.Vision.MaxTokens = 512
	}
	if c.VisionSlotsURL == "" {
		c.VisionSlotsURL = deriveSlotsURL(c.Vision.URL)
	}
	if c.SeedCount <= 0 {
		c.SeedCount = defaultSeedCount
	}
	if c.Infinity.Dimension <= 0 {
		c.Infinity.Dimension = defaultEmbeddingDimension
	}
}

// deriveSlotsURL turns the OpenAI-compatible base URL into llama-server's
// /slots, which is served from the root rather than from under /v1.
func deriveSlotsURL(base string) string {
	root := strings.TrimSuffix(strings.TrimRight(base, "/"), "/v1")
	if root == "" {
		return ""
	}
	return strings.TrimRight(root, "/") + "/slots"
}

const defaultSeedCount = 2000

// defaultEmbeddingDimension is jina-clip-v2's width. It must match the vector(N)
// columns pinned by migration 002.
const defaultEmbeddingDimension = 1024
