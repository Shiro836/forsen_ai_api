//go:build integration

// These tests talk to live services: 7TV, minio, magick and the llama-server on
// :3334. Everything that opens a Store additionally needs EMOTE_TEST_CONN, which
// has no default on purpose — see testConnStr.
package emoteservice

import (
	"context"
	"errors"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"app/pkg/infinity"
	"app/pkg/llm"
	"app/pkg/oai"
	"app/pkg/s3client"
	"app/pkg/seventv"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	forsenCreamID = "01J6KB8SBG00095HSJ1PN3XDPF"
	gigachadID    = "01F6MZGCNG000255K4X1K7NTHR"
	// Prayge is the most-used static emote, so it exercises the no-montage branch.
	praygeID = "01F6NACCD80006SZ7ZW5FMWKWK"

	testStreamerID     = "0198fa5c-0000-7000-8000-000000000001"
	testStreamerVecID  = "0198fa5c-0000-7000-8000-000000000002"
	testStreamerWorkID = "0198fa5c-0000-7000-8000-000000000003"
)

// testConnEnv names the database every store test runs against. It deliberately
// has no default: these tests delete rows and truncate whole tables, and the
// obvious default — the docker-compose postgres on 6445 — is the live registry
// the emote service is serving from. An unset variable skips; a variable
// pointing anywhere that is not named as a test database aborts.
const testConnEnv = "EMOTE_TEST_CONN"

func testConnStr(t *testing.T) string {
	t.Helper()
	conn := os.Getenv(testConnEnv)
	if conn == "" {
		t.Skipf("set %s to a disposable postgres to run the store tests; they truncate tables, so there is no default",
			testConnEnv)
	}

	// Belt and suspenders: exporting the production connection string must not be
	// enough to lose the registry.
	name := databaseName(conn)
	if !strings.Contains(strings.ToLower(name), "test") {
		t.Fatalf("%s points at database %q; destructive tests only run against a database whose name says it is one",
			testConnEnv, name)
	}
	return conn
}

// databaseName reads the database out of either connection string form pgx
// accepts: a URL, or space-separated keyword/value pairs.
func databaseName(conn string) string {
	if u, err := url.Parse(conn); err == nil && u.Scheme != "" {
		return strings.TrimPrefix(u.Path, "/")
	}
	for _, field := range strings.Fields(conn) {
		if name, ok := strings.CutPrefix(field, "dbname="); ok {
			return name
		}
	}
	return ""
}

// testVector pads a few leading components out to the width the embedding
// columns pin, so short vectors are not rejected.
func testVector(head ...float32) []float32 {
	v := make([]float32, defaultEmbeddingDimension)
	copy(v, head)
	return v
}

// testStore is the only way into the database from a test, which is what makes
// the guard in testConnStr unskippable.
func testStore(t *testing.T) (*Store, context.Context) {
	t.Helper()
	ctx := context.Background()
	store, err := NewStore(ctx, testConnStr(t))
	require.NoError(t, err)
	t.Cleanup(store.Close)
	require.NoError(t, store.Migrate(ctx))
	return store, ctx
}

// resetEmote clears every row a test writes so reruns start clean.
func resetEmote(t *testing.T, store *Store, ctx context.Context, emoteID, streamerID string) {
	t.Helper()
	_, err := store.pool.Exec(ctx, `DELETE FROM emote_rules WHERE streamer_id = $1`, streamerID)
	require.NoError(t, err)
	_, err = store.pool.Exec(ctx, `DELETE FROM emote_overrides WHERE emote_id = $1`, emoteID)
	require.NoError(t, err)
	_, err = store.pool.Exec(ctx, `DELETE FROM emote_streamer_settings WHERE streamer_id = $1`, streamerID)
	require.NoError(t, err)
	_, err = store.pool.Exec(ctx, `DELETE FROM emote_queue WHERE emote_id = $1`, emoteID)
	require.NoError(t, err)
	_, err = store.pool.Exec(ctx, `DELETE FROM emote_moderation WHERE emote_id = $1`, emoteID)
	require.NoError(t, err)
	_, err = store.pool.Exec(ctx, `DELETE FROM emote_overrides WHERE emote_id = $1`, emoteID)
	require.NoError(t, err)
}

func TestLiveStoreVerdictChain(t *testing.T) {
	store, ctx := testStore(t)
	const streamerID = testStreamerID
	resetEmote(t, store, ctx, forsenCreamID, streamerID)

	require.NoError(t, store.UpsertMetadata(ctx, &Emote{
		ID: forsenCreamID, Name: "forsenCream", Listed: true, Animated: true,
	}))

	settings, err := store.GetSettings(ctx, streamerID)
	require.NoError(t, err)
	assert.True(t, settings.Inherited, "an unconfigured channel inherits the platform default")
	assert.Contains(t, settings.BlockedClasses, ClassCum, "the inherited default blocks the cum class")

	facts, err := store.LoadFacts(ctx, streamerID, ProviderSevenTV, []string{forsenCreamID})
	require.NoError(t, err)
	assert.Equal(t, VerdictUnknown, Decide(facts[forsenCreamID], settings).Verdict)

	require.NoError(t, store.SaveClassification(ctx, ProviderSevenTV, forsenCreamID, &Classification{
		Description: "a man doused in white cream", Classes: []string{ClassCum},
	}, "qwen36-hauhau", ClassifierVersion))

	facts, err = store.LoadFacts(ctx, streamerID, ProviderSevenTV, []string{forsenCreamID})
	require.NoError(t, err)
	d := Decide(facts[forsenCreamID], settings)
	assert.Equal(t, VerdictBlock, d.Verdict)
	assert.Equal(t, ReasonClass+":"+ClassCum, d.Reason)

	require.NoError(t, store.UpsertSettings(ctx, Settings{
		StreamerID: streamerID, BlockedClasses: []string{ClassSexual},
	}))
	settings, err = store.GetSettings(ctx, streamerID)
	require.NoError(t, err)
	assert.False(t, settings.Inherited, "a materialized row overrides the platform default")
	facts, err = store.LoadFacts(ctx, streamerID, ProviderSevenTV, []string{forsenCreamID})
	require.NoError(t, err)
	assert.Equal(t, VerdictAllow, Decide(facts[forsenCreamID], settings).Verdict)

	require.NoError(t, store.AddOverride(ctx, Override{
		StreamerID: GlobalScope, EmoteID: forsenCreamID, Verdict: VerdictBlock, Author: "test",
	}))
	require.NoError(t, store.AddOverride(ctx, Override{
		StreamerID: streamerID, EmoteID: forsenCreamID, Verdict: VerdictAllow, Author: "test",
	}))
	facts, err = store.LoadFacts(ctx, streamerID, ProviderSevenTV, []string{forsenCreamID})
	require.NoError(t, err)
	assert.Equal(t, ReasonStreamerApprove, Decide(facts[forsenCreamID], settings).Reason)

	require.NoError(t, store.DeleteOverride(ctx, streamerID, ProviderSevenTV, forsenCreamID, VerdictAllow))
	facts, err = store.LoadFacts(ctx, streamerID, ProviderSevenTV, []string{forsenCreamID})
	require.NoError(t, err)
	assert.Equal(t, ReasonGlobalBan, Decide(facts[forsenCreamID], settings).Reason)
	require.NoError(t, store.DeleteOverride(ctx, GlobalScope, ProviderSevenTV, forsenCreamID, VerdictBlock))

	rule, err := store.CreateRule(ctx, streamerID, "ban cream emotes", nil)
	require.NoError(t, err)
	facts, err = store.LoadFacts(ctx, streamerID, ProviderSevenTV, []string{forsenCreamID})
	require.NoError(t, err)
	assert.Empty(t, facts[forsenCreamID].RuleID, "a draft rule enforces nothing")

	require.NoError(t, store.ConfirmRule(ctx, rule.ID, ProviderSevenTV, []string{forsenCreamID}))
	facts, err = store.LoadFacts(ctx, streamerID, ProviderSevenTV, []string{forsenCreamID})
	require.NoError(t, err)
	assert.Equal(t, ReasonRule+":"+rule.ID, Decide(facts[forsenCreamID], settings).Reason)

	resetEmote(t, store, ctx, forsenCreamID, streamerID)
}

func TestLivePgvectorScoring(t *testing.T) {
	store, ctx := testStore(t)
	const streamerID = testStreamerVecID
	resetEmote(t, store, ctx, forsenCreamID, streamerID)

	require.NoError(t, store.UpsertMetadata(ctx, &Emote{ID: forsenCreamID, Name: "forsenCream", Listed: true}))
	require.NoError(t, store.SaveClassification(ctx, ProviderSevenTV, forsenCreamID,
		&Classification{Description: "a man doused in white cream"}, "test", ClassifierVersion))
	require.NoError(t, store.SaveEmbeddings(ctx, ProviderSevenTV, forsenCreamID,
		testVector(1, 0, 0), testVector(0, 1, 0)))

	rows, err := store.ScoreEmotes(ctx, testVector(1, 0, 0), 0.9, 0.9, 10)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.Equal(t, SourceImage, rows[0].Source)
	assert.InDelta(t, 1.0, rows[0].Score, 1e-6)

	rows, err = store.ScoreEmotes(ctx, testVector(0.7, 0.7, 0), 0.5, 0.5, 10)
	require.NoError(t, err)
	assert.Len(t, rows, 2, "both signals fire above threshold")

	resetEmote(t, store, ctx, forsenCreamID, streamerID)
}

func TestLiveQueueRoundTrip(t *testing.T) {
	store, ctx := testStore(t)
	resetEmote(t, store, ctx, forsenCreamID, testStreamerID)

	// ErrQueueEmpty can only be asserted on an empty queue.
	_, err := store.pool.Exec(ctx, `DELETE FROM emote_queue`)
	require.NoError(t, err)

	n, err := store.Enqueue(ctx, ProviderSevenTV, []string{forsenCreamID}, 10, true)
	require.NoError(t, err)
	assert.Equal(t, 1, n)

	provider, claimed, force, err := store.ClaimNext(ctx)
	require.NoError(t, err)
	assert.Equal(t, ProviderSevenTV, provider)
	assert.Equal(t, forsenCreamID, claimed)
	assert.True(t, force, "an explicit enqueue asks for a re-judgement")

	require.NoError(t, store.MarkFailed(ctx, ProviderSevenTV, forsenCreamID, "boom", 3))
	_, claimed, force, err = store.ClaimNext(ctx)
	require.NoError(t, err)
	assert.Equal(t, forsenCreamID, claimed, "a failure under the attempt cap requeues")
	assert.True(t, force, "and the retry still forces")

	require.NoError(t, store.MarkDone(ctx, ProviderSevenTV, forsenCreamID))
	_, _, _, err = store.ClaimNext(ctx)
	assert.ErrorIs(t, err, ErrQueueEmpty)

	// Seeding is catch-up, not a re-judgement request, and must not inherit the
	// force a served row carried.
	_, err = store.Enqueue(ctx, ProviderSevenTV, []string{forsenCreamID}, 0, false)
	require.NoError(t, err)
	_, _, force, err = store.ClaimNext(ctx)
	require.NoError(t, err)
	assert.False(t, force, "force is cleared when the emote is done")

	_, err = store.pool.Exec(ctx, `DELETE FROM emote_queue WHERE emote_id = $1`, forsenCreamID)
	require.NoError(t, err)
}

// TestLiveWorkerProcessEmote runs the production pipeline for one emote:
// 7TV, minio, magick, the vision model and postgres.
func TestLiveWorkerProcessEmote(t *testing.T) {
	store, _ := testStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	const streamerID = testStreamerWorkID
	resetEmote(t, store, ctx, forsenCreamID, streamerID)

	cfg := &Config{Vision: visionConfigFromEnv(), TmpDir: os.TempDir()}
	cfg.withDefaults()

	objects, err := s3client.New(ctx, &s3client.Config{
		Endpoint:        envOr("EMOTE_TEST_S3_ENDPOINT", "localhost:9000"),
		AccessKeyID:     envOr("EMOTE_TEST_S3_KEY", "forsen"),
		SecretAccessKey: envOr("EMOTE_TEST_S3_SECRET", "forsen1337"),
	})
	require.NoError(t, err)

	client := seventv.New(&cfg.SevenTV)
	worker := NewWorker(
		slog.New(slog.NewTextHandler(os.Stderr, nil)),
		store,
		client,
		client,
		NewClassifier(integrationLogger(), oai.New(&cfg.Vision), cfg),
		nil,
		objects,
		cfg,
	)

	require.NoError(t, worker.ProcessEmote(ctx, ProviderSevenTV, forsenCreamID))

	emote, err := store.GetEmote(ctx, ProviderSevenTV, forsenCreamID)
	require.NoError(t, err)
	assert.Equal(t, "forsenCream", emote.Name)
	assert.True(t, emote.Listed)
	assert.True(t, emote.Animated)
	assert.True(t, emote.Classified())
	assert.NotEmpty(t, emote.Description)
	assert.Equal(t, "grid/"+forsenCreamID+".png", emote.GridKey)
	assert.Equal(t, "original/"+forsenCreamID+".webp", emote.OriginalKey)

	settings, err := store.GetSettings(ctx, streamerID)
	require.NoError(t, err)
	facts, err := store.LoadFacts(ctx, streamerID, ProviderSevenTV, []string{forsenCreamID})
	require.NoError(t, err)
	t.Logf("verdict: %+v (classes=%v version=%d)", Decide(facts[forsenCreamID], settings), emote.Classes, emote.ClassifierVersion)

	resetEmote(t, store, ctx, forsenCreamID, streamerID)
}

// TestLiveWorkerProcessesStaticEmote covers the branch that skips the montage.
func TestLiveWorkerProcessesStaticEmote(t *testing.T) {
	store, _ := testStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	resetEmote(t, store, ctx, praygeID, testStreamerWorkID)

	cfg := &Config{Vision: visionConfigFromEnv(), TmpDir: os.TempDir()}
	cfg.withDefaults()

	client := seventv.New(&cfg.SevenTV)
	meta, err := client.GetEmote(ctx, praygeID)
	require.NoError(t, err)
	require.Equal(t, 1, meta.FrameCount(seventv.File4x), "Prayge must be static for this test to mean anything")

	worker := NewWorker(
		slog.New(slog.NewTextHandler(os.Stderr, nil)),
		store, client, client,
		NewClassifier(integrationLogger(), oai.New(&cfg.Vision), cfg),
		nil, nil, cfg,
	)
	require.NoError(t, worker.ProcessEmote(ctx, ProviderSevenTV, praygeID))

	emote, err := store.GetEmote(ctx, ProviderSevenTV, praygeID)
	require.NoError(t, err)
	assert.Equal(t, "Prayge", emote.Name)
	assert.True(t, emote.Classified())
	assert.NotEmpty(t, emote.Description)
	t.Logf("static classification: %q classes=%v", emote.Description, emote.Classes)

	resetEmote(t, store, ctx, praygeID, testStreamerWorkID)
}

// stubEmbedder stands in for Infinity, which is not deployed yet, so the write
// path for both vectors is still exercised end to end.
type stubEmbedder struct{}

func (stubEmbedder) EmbedText(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, 0, len(texts))
	for range texts {
		out = append(out, testVector(0, 1, 0))
	}
	return out, nil
}

func (stubEmbedder) EmbedPNGs(_ context.Context, images [][]byte) ([][]float32, error) {
	out := make([][]float32, 0, len(images))
	for range images {
		out = append(out, testVector(1, 0, 0))
	}
	return out, nil
}

// TestLiveWorkerWritesEmbeddings runs the whole pipeline with vectors attached
// and then matches a rule against what it wrote.
func TestLiveWorkerWritesEmbeddings(t *testing.T) {
	store, _ := testStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	resetEmote(t, store, ctx, forsenCreamID, testStreamerWorkID)

	cfg := &Config{Vision: visionConfigFromEnv(), TmpDir: os.TempDir()}
	cfg.withDefaults()

	client := seventv.New(&cfg.SevenTV)
	embedder := stubEmbedder{}
	worker := NewWorker(
		slog.New(slog.NewTextHandler(os.Stderr, nil)),
		store, client, client,
		NewClassifier(integrationLogger(), oai.New(&cfg.Vision), cfg),
		embedder, nil, cfg,
	)
	require.NoError(t, worker.ProcessEmote(ctx, ProviderSevenTV, forsenCreamID))

	candidates, err := NewRuleMatcher(embedder, store, cfg).Candidates(ctx, "cream", nil)
	require.NoError(t, err)
	require.Len(t, candidates, 1)
	assert.Equal(t, forsenCreamID, candidates[0].EmoteID)
	assert.Equal(t, []string{SourceDescription}, candidates[0].Sources,
		"the stub puts the rule vector on the description axis only")

	resetEmote(t, store, ctx, forsenCreamID, testStreamerWorkID)
}

// failingEmbedder stands in for an embedding server that is down.
type failingEmbedder struct{}

func (failingEmbedder) EmbedText(context.Context, []string) ([][]float32, error) {
	return nil, errors.New("infinity down")
}

func (failingEmbedder) EmbedPNGs(context.Context, [][]byte) ([][]float32, error) {
	return nil, errors.New("infinity down")
}

// TestLiveEmbedderOutageKeepsTheClassification pins the split: an embedder that
// is down must not cost a second pass over the vision model.
func TestLiveEmbedderOutageKeepsTheClassification(t *testing.T) {
	store, _ := testStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	resetEmote(t, store, ctx, praygeID, testStreamerWorkID)

	cfg := &Config{Vision: visionConfigFromEnv(), TmpDir: os.TempDir()}
	cfg.withDefaults()
	client := seventv.New(&cfg.SevenTV)
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	classifier := NewClassifier(integrationLogger(), oai.New(&cfg.Vision), cfg)

	broken := NewWorker(logger, store, client, client, classifier, failingEmbedder{}, nil, cfg)
	require.Error(t, broken.ProcessEmote(ctx, ProviderSevenTV, praygeID), "the embedder failure must surface")

	afterFailure, err := store.GetEmote(ctx, ProviderSevenTV, praygeID)
	require.NoError(t, err)
	require.True(t, afterFailure.Classified(), "the vision verdict survives the embedder outage")

	recovered := NewWorker(logger, store, client, client, classifier, stubEmbedder{}, nil, cfg)
	require.NoError(t, recovered.ProcessEmote(ctx, ProviderSevenTV, praygeID))

	afterRetry, err := store.GetEmote(ctx, ProviderSevenTV, praygeID)
	require.NoError(t, err)
	assert.Equal(t, *afterFailure.ClassifiedAt, *afterRetry.ClassifiedAt,
		"the retry reuses the cached verdict instead of calling the model again")

	candidates, err := NewRuleMatcher(stubEmbedder{}, store, cfg).Candidates(ctx, "prayer", nil)
	require.NoError(t, err)
	require.Len(t, candidates, 1, "the retry filled in the missing vectors")

	resetEmote(t, store, ctx, praygeID, testStreamerWorkID)
}

// TestLiveInfinityEmbeddings talks to a real Infinity server when one is up.
func TestLiveInfinityEmbeddings(t *testing.T) {
	cfg := infinity.Config{
		URL:       envOr("EMOTE_TEST_INFINITY_URL", "http://localhost:7997"),
		Model:     envOr("EMOTE_TEST_INFINITY_MODEL", "jinaai/jina-clip-v2"),
		Dimension: defaultEmbeddingDimension,
		Timeout:   3 * time.Minute,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	client := infinity.New(&cfg)
	vecs, err := client.EmbedText(ctx, []string{"a man doused in white cream"})
	if err != nil {
		t.Skipf("infinity not reachable at %s: %v", cfg.URL, err)
	}
	require.Len(t, vecs, 1)
	assert.Len(t, vecs[0], defaultEmbeddingDimension)

	grid, err := os.ReadFile(envOr("EMOTE_TEST_GRID_PNG", ""))
	if err != nil {
		t.Skip("set EMOTE_TEST_GRID_PNG to a grid png to exercise the image modality")
	}
	images, err := client.EmbedImages(ctx, []infinity.Image{{MIME: "image/png", Data: grid}})
	require.NoError(t, err)
	require.Len(t, images, 1)
	assert.Len(t, images[0], defaultEmbeddingDimension, "text and image embeddings share one space")
}

// TestLiveSeedEnqueuesTopEmotes runs the real v4 GQL popularity crawl.
func TestLiveSeedEnqueuesTopEmotes(t *testing.T) {
	store, _ := testStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	cfg := &Config{TmpDir: os.TempDir()}
	cfg.withDefaults()

	client := seventv.New(&cfg.SevenTV)
	worker := NewWorker(slog.New(slog.NewTextHandler(os.Stderr, nil)), store, client, client, nil, nil, nil, cfg)

	queued, err := worker.Seed(ctx, []SeedSource{{Count: 300}}, 0)
	require.NoError(t, err)
	assert.Greater(t, queued, 300, "two deduplicated pages, plus the global set on top of them")

	top, err := store.GetEmote(ctx, ProviderSevenTV, gigachadID)
	require.NoError(t, err)
	assert.Equal(t, "GIGACHAD", top.Name, "seeding records metadata before classification")
	assert.True(t, top.Listed)

	// The regression this guards: the global set renders in every channel and is
	// curated rather than ranked, so no top-N depth ever contains it.
	global, err := client.GetGlobalSet(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, global.Emotes)
	var missing []string
	for _, entry := range global.Emotes {
		if _, err := store.GetEmote(ctx, ProviderSevenTV, entry.EmoteID); errors.Is(err, ErrNotFound) {
			missing = append(missing, entry.Name)
		}
	}
	assert.Empty(t, missing, "every global-set emote is seeded")

	// A query-scoped source overlaps the global head, so the union must grow by
	// less than it fetched.
	total, err := worker.Seed(ctx, []SeedSource{{Count: 300}, {Query: "forsen", Count: 300}}, 0)
	require.NoError(t, err)
	assert.Greater(t, total, 300, "the forsen slice adds emotes the global head misses")
	assert.LessOrEqual(t, total, 600+len(global.Emotes))

	var forsenNamed int
	require.NoError(t, store.pool.QueryRow(ctx,
		`SELECT count(*) FROM emote_moderation WHERE lower(name) LIKE 'forsen%'`).Scan(&forsenNamed))
	assert.NotZero(t, forsenNamed, "query-scoped seeding is what reaches a channel's own emotes")

	// Seeding writes hundreds of rows this test never names, so the only cleanup
	// available is wholesale — which is the reason the store tests refuse to run
	// against anything but a database named as a test one.
	_, err = store.pool.Exec(ctx, `DELETE FROM emote_queue`)
	require.NoError(t, err)
	_, err = store.pool.Exec(ctx, `DELETE FROM emote_moderation`)
	require.NoError(t, err)
}

func TestLiveSevenTVSets(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	client := seventv.New(&seventv.Config{})

	global, err := client.GetGlobalSet(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, global.Emotes)

	var aliased, zeroWidth int
	for _, e := range global.Emotes {
		if e.Aliased() {
			aliased++
		}
		if e.ZeroWidth {
			zeroWidth++
		}
	}
	assert.NotZero(t, aliased, "even the global set is aliased, so alias handling cannot be skipped")
	t.Logf("global set: %d emotes, %d aliased, %d zero-width", len(global.Emotes), aliased, zeroWidth)

	user, err := client.GetTwitchUser(ctx, "71092938")
	require.NoError(t, err)
	assert.Equal(t, "xqc", user.Username)
	assert.NotEmpty(t, user.Set.Emotes)

	// forsen has never linked 7TV; that is an ordinary outcome, not a failure.
	_, err = client.GetTwitchUser(ctx, "22484632")
	assert.ErrorIs(t, err, seventv.ErrNoAccount)
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// TestLiveClassifyForsenCream is the end-to-end smoke test: real 7TV metadata
// and CDN image, real magick montage, real vision model.
func TestLiveClassifyForsenCream(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	cfg := &Config{
		Vision: visionConfigFromEnv(),
		TmpDir: os.TempDir(),
	}
	cfg.withDefaults()

	client := seventv.New(&cfg.SevenTV)
	meta, err := client.GetEmote(ctx, forsenCreamID)
	require.NoError(t, err)
	require.Equal(t, "forsenCream", meta.Name)
	require.True(t, meta.Animated)

	webp, err := client.DownloadImage(ctx, forsenCreamID, seventv.File4x)
	require.NoError(t, err)
	t.Logf("downloaded %d bytes", len(webp))

	classifier := NewClassifier(
		integrationLogger(),
		oai.New(&cfg.Vision),
		cfg)

	grid, err := classifier.BuildGrid(ctx, webp)
	require.NoError(t, err)
	assert.Equal(t, 60, grid.TotalFrames)
	assert.Equal(t, 10, grid.Tiles)

	started := time.Now()
	got, err := classifier.Classify(ctx, meta.Name, grid)
	require.NoError(t, err)
	t.Logf("classified in %s: %+v", time.Since(started), *got)

	assert.NotEmpty(t, got.Description)
	assert.Contains(t, got.Classes, ClassCum, "forsenCream is the canonical cum-class positive from the ADR")
}

// The measure both sets and clears the flashing class, and must leave every
// other class the vision model assigned exactly where it was.
func TestLiveFlashMeasureOwnsTheFlashingClass(t *testing.T) {
	store, ctx := testStore(t)
	resetEmote(t, store, ctx, forsenCreamID, testStreamerID)

	require.NoError(t, store.UpsertMetadata(ctx, &Emote{ID: forsenCreamID, Name: "forsenCream", Animated: true}))
	require.NoError(t, store.SaveClassification(ctx, ProviderSevenTV, forsenCreamID, &Classification{
		Description: "a man doused in white cream", Classes: []string{ClassCum},
	}, "qwen36-hauhau", ClassifierVersion))

	hazard := FlashMeasure{Hz: 17, HzMax: 17, RedHz: 2}
	require.NoError(t, store.SaveFlashMeasure(ctx, ProviderSevenTV, forsenCreamID, hazard))
	emote, err := store.GetEmote(ctx, ProviderSevenTV, forsenCreamID)
	require.NoError(t, err)
	assert.Equal(t, []string{ClassCum, ClassFlashing}, emote.Classes)
	assert.Equal(t, 17.0, emote.FlashHz)
	assert.Equal(t, 2.0, emote.RedFlashHz)
	assert.NotNil(t, emote.FlashMeasuredAt)

	require.NoError(t, store.SaveFlashMeasure(ctx, ProviderSevenTV, forsenCreamID, FlashMeasure{Hz: 1}))
	emote, err = store.GetEmote(ctx, ProviderSevenTV, forsenCreamID)
	require.NoError(t, err)
	assert.Equal(t, []string{ClassCum}, emote.Classes)
	assert.Equal(t, ClassifierVersion, emote.ClassifierVersion, "re-measuring is not a reclassification")
	assert.Equal(t, "a man doused in white cream", emote.Description)

	resetEmote(t, store, ctx, forsenCreamID, testStreamerID)
}

func integrationLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, nil))
}

func visionConfigFromEnv() llm.Config {
	url := os.Getenv("EMOTE_TEST_VISION_URL")
	if url == "" {
		url = "http://localhost:3334/v1"
	}
	model := os.Getenv("EMOTE_TEST_VISION_MODEL")
	if model == "" {
		model = "qwen36-hauhau"
	}
	return llm.Config{URL: url, Model: model, MaxTokens: 512}
}
