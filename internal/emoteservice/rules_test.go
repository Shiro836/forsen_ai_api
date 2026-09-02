package emoteservice

import (
	"context"
	"errors"
	"math"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeEmbedder maps known strings to fixed vectors so a test can pin what a
// rule is compared against without an embedding server.
type fakeEmbedder struct {
	vectors map[string][]float32
	seen    []string
	err     error
}

func (f *fakeEmbedder) EmbedText(_ context.Context, texts []string) ([][]float32, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := make([][]float32, 0, len(texts))
	for _, t := range texts {
		f.seen = append(f.seen, t)
		v, ok := f.vectors[t]
		if !ok {
			return nil, errors.New("fakeEmbedder: unknown text " + t)
		}
		out = append(out, v)
	}
	return out, nil
}

func (f *fakeEmbedder) EmbedPNGs(context.Context, [][]byte) ([][]float32, error) {
	return nil, errors.New("fakeEmbedder: images not configured")
}

// fakeVectors scores in Go what the store scores in pgvector, so the threshold
// and union behaviour under test is the real one.
type fakeVectors struct {
	emotes []fakeEmote
}

type fakeEmote struct {
	id    string
	name  string
	desc  string
	image []float32
	text  []float32
}

func (f *fakeVectors) ScoreEmotes(_ context.Context, vec []float32, imageThreshold, descThreshold float64, limit int) ([]CandidateRow, error) {
	var rows []CandidateRow
	for _, e := range f.emotes {
		if e.image != nil {
			if score := cosine(vec, e.image); score >= imageThreshold {
				rows = append(rows, CandidateRow{EmoteID: e.id, Name: e.name, Description: e.desc, Source: SourceImage, Score: score})
			}
		}
		if e.text != nil {
			if score := cosine(vec, e.text); score >= descThreshold {
				rows = append(rows, CandidateRow{EmoteID: e.id, Name: e.name, Description: e.desc, Source: SourceDescription, Score: score})
			}
		}
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Score > rows[j].Score })
	if limit > 0 && len(rows) > limit {
		rows = rows[:limit]
	}
	return rows, nil
}

func cosine(a, b []float32) float64 {
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

func testMatcher(embedder Embedder, source VectorSource) *RuleMatcher {
	cfg := &Config{ImageThreshold: 0.5, DescThreshold: 0.5, RuleCandidates: 10}
	return NewRuleMatcher(embedder, source, cfg)
}

func TestRuleMatcherUnionsImageAndDescriptionSignals(t *testing.T) {
	embedder := &fakeEmbedder{vectors: map[string][]float32{
		"ban all feet emotes": {1, 0, 0},
	}}
	source := &fakeVectors{emotes: []fakeEmote{
		{id: "seen-only-in-pixels", name: "footEmote", image: []float32{1, 0, 0}, text: []float32{0, 1, 0}},
		{id: "seen-only-in-words", name: "toesUp", desc: "a close-up of bare feet", image: []float32{0, 0, 1}, text: []float32{0.9, 0.1, 0}},
		{id: "seen-in-both", name: "feetGasm", image: []float32{0.8, 0.6, 0}, text: []float32{0.7, 0.7, 0}},
		{id: "unrelated", name: "pepeLaugh", image: []float32{0, 1, 0}, text: []float32{0, 0, 1}},
	}}

	got, err := testMatcher(embedder, source).Candidates(context.Background(), "ban all feet emotes", nil)
	require.NoError(t, err)

	byID := map[string]Candidate{}
	for _, c := range got {
		byID[c.EmoteID] = c
	}
	require.Len(t, got, 3, "unrelated emote must not be a candidate")
	assert.Equal(t, []string{SourceImage}, byID["seen-only-in-pixels"].Sources)
	assert.Equal(t, []string{SourceDescription}, byID["seen-only-in-words"].Sources)
	assert.Equal(t, []string{SourceDescription, SourceImage}, byID["seen-in-both"].Sources)
}

// Templating the rule into a caption was measured to hurt some rules, so the
// streamer's text must reach the embedder untouched.
func TestRuleTextIsEmbeddedVerbatim(t *testing.T) {
	rule := "ban all feet emotes"
	embedder := &fakeEmbedder{vectors: map[string][]float32{rule: {1, 0, 0}}}

	_, err := testMatcher(embedder, &fakeVectors{}).Candidates(context.Background(), rule, nil)
	require.NoError(t, err)
	assert.Equal(t, []string{rule}, embedder.seen)
}

func TestRuleMatcherOrdersByBestScore(t *testing.T) {
	embedder := &fakeEmbedder{vectors: map[string][]float32{"anime": {1, 0}}}
	source := &fakeVectors{emotes: []fakeEmote{
		{id: "weak", image: []float32{0.6, 0.8}},
		{id: "strong", image: []float32{1, 0}},
		{id: "middle", image: []float32{0.8, 0.6}},
	}}

	got, err := testMatcher(embedder, source).Candidates(context.Background(), "anime", nil)
	require.NoError(t, err)

	ids := make([]string, 0, len(got))
	for _, c := range got {
		ids = append(ids, c.EmoteID)
	}
	assert.Equal(t, []string{"strong", "middle", "weak"}, ids)
}

func TestRuleMatcherKeepsBestScoreAcrossSignals(t *testing.T) {
	embedder := &fakeEmbedder{vectors: map[string][]float32{"politics": {1, 0}}}
	source := &fakeVectors{emotes: []fakeEmote{
		{id: "both", image: []float32{0.6, 0.8}, text: []float32{1, 0}},
	}}

	got, err := testMatcher(embedder, source).Candidates(context.Background(), "politics", nil)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.InDelta(t, 1.0, got[0].Score, 1e-9)
}

func TestRuleMatcherPerRuleThresholdOverridesTheDefault(t *testing.T) {
	embedder := &fakeEmbedder{vectors: map[string][]float32{"anime": {1, 0}}}
	source := &fakeVectors{emotes: []fakeEmote{
		{id: "weak", image: []float32{0.6, 0.8}},
		{id: "strong", image: []float32{1, 0}},
	}}
	matcher := testMatcher(embedder, source)

	got, err := matcher.Candidates(context.Background(), "anime", nil)
	require.NoError(t, err)
	require.Len(t, got, 2, "the 0.5 default admits both")

	strict := 0.9
	got, err = matcher.Candidates(context.Background(), "anime", &strict)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "strong", got[0].EmoteID)
}

func TestRuleMatcherWithoutEmbedder(t *testing.T) {
	_, err := testMatcher(nil, &fakeVectors{}).Candidates(context.Background(), "feet", nil)
	assert.ErrorIs(t, err, ErrEmbedderUnavailable)
}

func TestRuleMatcherPropagatesEmbedderFailure(t *testing.T) {
	embedder := &fakeEmbedder{err: errors.New("infinity down")}
	_, err := testMatcher(embedder, &fakeVectors{}).Candidates(context.Background(), "feet", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "infinity down")
}

// Search shares the rule matcher's union-by-best-score merge but drops the
// cutoff, so a weak match is ranked last instead of disappearing.
func TestSearchRanksEverythingWithoutACutoff(t *testing.T) {
	embedder := &fakeEmbedder{vectors: map[string][]float32{"anime": {1, 0}}}
	source := &fakeVectors{emotes: []fakeEmote{
		{id: "orthogonal", name: "pepeLaugh", image: []float32{0, 1}},
		{id: "strong", name: "animeGirl", image: []float32{1, 0}},
		{id: "middle", name: "mangaPog", image: []float32{0.8, 0.6}},
	}}
	matcher := testMatcher(embedder, source)

	rules, err := matcher.Candidates(context.Background(), "anime", nil)
	require.NoError(t, err)
	require.Len(t, rules, 2, "the 0.5 rule threshold drops the orthogonal emote")

	got, err := matcher.Search(context.Background(), "anime", 10)
	require.NoError(t, err)

	ids := make([]string, 0, len(got))
	for _, c := range got {
		ids = append(ids, c.EmoteID)
	}
	assert.Equal(t, []string{"strong", "middle", "orthogonal"}, ids)
	assert.InDelta(t, 1.0, got[0].Score, 1e-9)
	assert.Equal(t, []string{"anime"}, embedder.seen[1:], "the query must reach the embedder verbatim")
}

func TestSearchTakesTheBestOfBothSignals(t *testing.T) {
	embedder := &fakeEmbedder{vectors: map[string][]float32{"politics": {1, 0}}}
	source := &fakeVectors{emotes: []fakeEmote{
		{id: "words-only", image: []float32{0, 1}, text: []float32{1, 0}},
	}}

	got, err := testMatcher(embedder, source).Search(context.Background(), "politics", 10)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.InDelta(t, 1.0, got[0].Score, 1e-9, "the stronger signal decides the rank")
	assert.Equal(t, []string{SourceDescription, SourceImage}, got[0].Sources)
}

func TestSearchTruncatesToTheRequestedPage(t *testing.T) {
	embedder := &fakeEmbedder{vectors: map[string][]float32{"anime": {1, 0}}}
	source := &fakeVectors{emotes: []fakeEmote{
		{id: "a", image: []float32{1, 0}},
		{id: "b", image: []float32{0.9, 0.1}},
		{id: "c", image: []float32{0.8, 0.2}},
	}}

	got, err := testMatcher(embedder, source).Search(context.Background(), "anime", 2)
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, "a", got[0].EmoteID)
	assert.Equal(t, "b", got[1].EmoteID)
}

func TestSearchWithoutEmbedder(t *testing.T) {
	_, err := testMatcher(nil, &fakeVectors{}).Search(context.Background(), "anime", 10)
	assert.ErrorIs(t, err, ErrEmbedderUnavailable)
}

func TestMergeCandidatesKeepsProvidersApart(t *testing.T) {
	got := mergeCandidates([]CandidateRow{
		{Provider: "7tv", EmoteID: "same", Source: SourceImage, Score: 0.7},
		{Provider: "bttv", EmoteID: "same", Source: SourceImage, Score: 0.6},
	})
	require.Len(t, got, 2, "an id collision across providers is two emotes")
	assert.Equal(t, "7tv", got[0].Provider)
	assert.Equal(t, "bttv", got[1].Provider)
}

func TestMergeCandidatesBreaksTiesByEmoteID(t *testing.T) {
	got := mergeCandidates([]CandidateRow{
		{EmoteID: "b", Source: SourceImage, Score: 0.5},
		{EmoteID: "a", Source: SourceImage, Score: 0.5},
	})
	require.Len(t, got, 2)
	assert.Equal(t, "a", got[0].EmoteID)
	assert.Equal(t, "b", got[1].EmoteID)
}

func TestSampleIndices(t *testing.T) {
	assert.Equal(t, []int{0, 6, 12, 18, 24, 30, 36, 42, 48, 54}, sampleIndices(60, 10))
	assert.Equal(t, []int{0, 1, 2}, sampleIndices(3, 10))
	assert.Equal(t, []int{0}, sampleIndices(1, 10))
	assert.Equal(t, []int{0, 3, 7}, sampleIndices(11, 3))
}
