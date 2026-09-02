package emoteservice

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"app/pkg/seventv"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakePopular answers the popularity crawl per query, ignoring depth: the point
// of these tests is what a top-N never contains.
type fakePopular map[string][]seventv.Emote

func (f fakePopular) TopEmotes(_ context.Context, query string, _ int) ([]seventv.Emote, error) {
	return f[query], nil
}

type fakeEmoteSource struct {
	global []seventv.ActiveEmote
	err    error
}

func (f fakeEmoteSource) GetGlobalSet(context.Context) (*seventv.EmoteSet, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &seventv.EmoteSet{ID: seventv.GlobalEmoteSet, Emotes: f.global}, nil
}

func (fakeEmoteSource) GetEmote(context.Context, string) (*seventv.Emote, error) {
	return nil, errors.New("not used")
}

func (fakeEmoteSource) DownloadImage(context.Context, string, string) ([]byte, error) {
	return nil, errors.New("not used")
}

func seedWorker(source EmoteSource, popular seventv.PopularSource) *Worker {
	return &Worker{
		logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		source:    source,
		popular:   popular,
		seedCount: 10,
	}
}

func crawledIDs(emotes []seventv.Emote) []string {
	out := make([]string, 0, len(emotes))
	for _, e := range emotes {
		out = append(out, e.ID)
	}
	return out
}

// The global set renders in every channel and no popularity crawl reaches it, so
// a seed run that only followed its configured sources would never classify it.
func TestSeedCrawlAlwaysIncludesTheGlobalSet(t *testing.T) {
	worker := seedWorker(
		fakeEmoteSource{global: []seventv.ActiveEmote{
			{EmoteID: "global1", Emote: seventv.Emote{Name: "PauseChamp", Listed: true}},
			{EmoteID: "global2", Emote: seventv.Emote{Name: "Clap", Listed: true}},
			{EmoteID: "top2", Emote: seventv.Emote{Name: "GIGACHAD"}},
		}},
		fakePopular{
			"":       {{ID: "top1"}, {ID: "top2"}},
			"forsen": {{ID: "top2"}, {ID: "top3"}},
		},
	)

	emotes, err := worker.crawlSeedSources(context.Background(),
		[]SeedSource{{Count: 2}, {Query: "forsen", Count: 2}})

	require.NoError(t, err)
	assert.Equal(t, []string{"global1", "global2", "top2", "top1", "top3"}, crawledIDs(emotes),
		"the global set leads and every id appears once")
}

func TestSeedCrawlIncludesTheGlobalSetWithNoConfiguredSources(t *testing.T) {
	worker := seedWorker(
		fakeEmoteSource{global: []seventv.ActiveEmote{{EmoteID: "global1"}}},
		fakePopular{},
	)

	emotes, err := worker.crawlSeedSources(context.Background(), nil)

	require.NoError(t, err)
	assert.Equal(t, []string{"global1"}, crawledIDs(emotes))
}

// A set entry carries the emote's own metadata, so the global set needs no extra
// round trip to be recorded — but the id must come from the entry.
func TestSeedCrawlTakesGlobalMetadataFromTheSetEntry(t *testing.T) {
	worker := seedWorker(
		fakeEmoteSource{global: []seventv.ActiveEmote{{
			EmoteID: "global1",
			Name:    "channelAlias",
			Emote:   seventv.Emote{Name: "PauseChamp", Listed: true, Animated: true},
		}}},
		fakePopular{},
	)

	emotes, err := worker.crawlSeedSources(context.Background(), nil)

	require.NoError(t, err)
	require.Len(t, emotes, 1)
	assert.Equal(t, "global1", emotes[0].ID)
	assert.Equal(t, "PauseChamp", emotes[0].Name, "the emote's own name, not the set's alias")
	assert.True(t, emotes[0].Animated)
}

// Seeding everything except the one set that renders everywhere is the bug this
// exists to prevent, so the run fails rather than quietly skipping it.
func TestSeedCrawlFailsWhenTheGlobalSetCannotBeFetched(t *testing.T) {
	worker := seedWorker(
		fakeEmoteSource{err: errors.New("7tv down")},
		fakePopular{"": {{ID: "top1"}}},
	)

	_, err := worker.crawlSeedSources(context.Background(), []SeedSource{{Count: 1}})

	require.ErrorContains(t, err, "global set")
}
