package emoteservice

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"
)

const (
	SourceImage       = "image"
	SourceDescription = "description"
)

// ErrEmbedderUnavailable is returned when a rule is submitted while no
// embedding server is configured. Rules are the only feature that needs one.
var ErrEmbedderUnavailable = errors.New("emoteservice: embedder not configured")

// Embedder produces vectors in the shared image/text space a rule is matched in.
type Embedder interface {
	EmbedText(ctx context.Context, texts []string) ([][]float32, error)
	EmbedPNGs(ctx context.Context, images [][]byte) ([][]float32, error)
}

// CandidateRow is one emote scored against a rule vector by a single signal.
type CandidateRow struct {
	Provider    string
	EmoteID     string
	Name        string
	Description string
	GridKey     string
	Source      string
	Score       float64
}

// VectorSource scores a rule vector against the cached emote embeddings.
type VectorSource interface {
	ScoreEmotes(ctx context.Context, vec []float32, imageThreshold, descThreshold float64, limit int) ([]CandidateRow, error)
}

// Candidate is one emote a rule proposes to block, ready for the streamer's
// confirm-preview.
type Candidate struct {
	Provider    string   `json:"provider"`
	EmoteID     string   `json:"emote_id"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	GridKey     string   `json:"grid_key,omitempty"`
	Sources     []string `json:"sources"`
	Score       float64  `json:"score"`
}

type Rule struct {
	ID         string    `json:"id"`
	StreamerID string    `json:"streamer_id"`
	RuleText   string    `json:"rule_text"`
	Threshold  *float64  `json:"threshold,omitempty"`
	Status     string    `json:"status"`
	MatchCount int       `json:"match_count"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type RuleMatcher struct {
	embedder       Embedder
	source         VectorSource
	imageThreshold float64
	descThreshold  float64
	limit          int
}

func NewRuleMatcher(embedder Embedder, source VectorSource, cfg *Config) *RuleMatcher {
	return &RuleMatcher{
		embedder:       embedder,
		source:         source,
		imageThreshold: cfg.ImageThreshold,
		descThreshold:  cfg.DescThreshold,
		limit:          cfg.RuleCandidates,
	}
}

// noCutoff admits every scored row, cosine similarity being bounded below by -1.
const noCutoff = -1.0

// Candidates embeds a free-text rule and returns the emotes it matches, best
// first. Nothing is enforced from this list until a human confirms it.
//
// A rule may carry its own cutoff, because no single default works: the usable
// cosine band is narrow, "anime" separates cleanly from everything else while
// "feet" does not separate on any model measured, and the defaults in Config are
// a starting point awaiting a labelled validation set. The confirm-preview, not
// the threshold, is what keeps a bad rule from blocking emotes.
func (m *RuleMatcher) Candidates(ctx context.Context, ruleText string, threshold *float64) ([]Candidate, error) {
	imageThreshold, descThreshold := m.imageThreshold, m.descThreshold
	if threshold != nil {
		imageThreshold, descThreshold = *threshold, *threshold
	}

	return m.rank(ctx, ruleText, imageThreshold, descThreshold, m.limit)
}

// Search ranks the classified registry against free text. It applies no cutoff,
// unlike a rule: a search answers with an ordered list the reader judges, so a
// threshold would only hide the tail without protecting anything.
func (m *RuleMatcher) Search(ctx context.Context, query string, limit int) ([]Candidate, error) {
	// Each emote can contribute two rows to the merge, so the row budget is
	// doubled to keep a full page of emotes after they fold together.
	out, err := m.rank(ctx, query, noCutoff, noCutoff, limit*2)
	if err != nil {
		return nil, err
	}
	if len(out) > limit {
		out = out[:limit]
	}

	return out, nil
}

func (m *RuleMatcher) rank(ctx context.Context, text string, imageThreshold, descThreshold float64, limit int) ([]Candidate, error) {
	if m.embedder == nil {
		return nil, ErrEmbedderUnavailable
	}

	// The user's words go to the embedder verbatim. Prompt templating ("a photo
	// of X") was measured to be no reliable gain and to hurt some rules outright.
	vecs, err := m.embedder.EmbedText(ctx, []string{text})
	if err != nil {
		return nil, fmt.Errorf("embed text: %w", err)
	}
	if len(vecs) != 1 || len(vecs[0]) == 0 {
		return nil, fmt.Errorf("embed text: embedder returned no vector")
	}

	rows, err := m.source.ScoreEmotes(ctx, vecs[0], imageThreshold, descThreshold, limit)
	if err != nil {
		return nil, fmt.Errorf("score emotes: %w", err)
	}

	return mergeCandidates(rows), nil
}

// mergeCandidates folds the per-signal scores into one row per emote — the ADR
// takes the union of the image and description matches, not their intersection.
func mergeCandidates(rows []CandidateRow) []Candidate {
	byID := make(map[string]*Candidate, len(rows))
	order := make([]string, 0, len(rows))

	for _, r := range rows {
		key := r.Provider + "/" + r.EmoteID
		c, ok := byID[key]
		if !ok {
			c = &Candidate{
				Provider:    r.Provider,
				EmoteID:     r.EmoteID,
				Name:        r.Name,
				Description: r.Description,
				GridKey:     r.GridKey,
				Score:       r.Score,
			}
			byID[key] = c
			order = append(order, key)
		}
		if r.Score > c.Score {
			c.Score = r.Score
		}
		if !containsString(c.Sources, r.Source) {
			c.Sources = append(c.Sources, r.Source)
		}
	}

	out := make([]Candidate, 0, len(order))
	for _, key := range order {
		c := byID[key]
		sort.Strings(c.Sources)
		out = append(out, *c)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].EmoteID < out[j].EmoteID
	})
	return out
}

func containsString(s []string, v string) bool {
	for _, e := range s {
		if e == v {
			return true
		}
	}
	return false
}
