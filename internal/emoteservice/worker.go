package emoteservice

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync/atomic"
	"time"

	"app/pkg/s3client"
	"app/pkg/seventv"

	"golang.org/x/sync/errgroup"
)

// ObjectStore is the subset of the s3 client the cache builder uses: originals
// and grids go in, and the flash backfill reads originals back out.
type ObjectStore interface {
	PutObject(ctx context.Context, bucket, objectName string, reader io.Reader, size int64, contentType string) error
	GetObject(ctx context.Context, bucket, objectName string) (io.ReadCloser, error)
}

// ErrSeedingUnavailable is returned when the popularity crawl has no source.
var ErrSeedingUnavailable = errors.New("emoteservice: no popular emote source configured")

// SeedSource is one popularity crawl. An empty Query takes the global top list;
// a query takes the top of that slice, which is how a channel's own emotes get
// seeded without crawling the channel.
type SeedSource struct {
	Query string `json:"query" yaml:"query"`
	Count int    `json:"count" yaml:"count"`
}

// EmoteSource resolves 7TV metadata and images, and the global set every seed
// run includes.
type EmoteSource interface {
	GetEmote(ctx context.Context, id string) (*seventv.Emote, error)
	DownloadImage(ctx context.Context, id, file string) ([]byte, error)
	GetGlobalSet(ctx context.Context) (*seventv.EmoteSet, error)
}

// emptyQueuePoll is how long the loop waits before asking for work again. It
// only ever runs when there is nothing to claim.
const emptyQueuePoll = time.Second

// workerStore is the slice of Store the drain pipeline goes through. It is a
// test seam: the pipeline's stages run concurrently, and this is what lets that
// be exercised under -race without a database.
type workerStore interface {
	ClaimNext(ctx context.Context) (provider, emoteID string, force bool, err error)
	MarkDone(ctx context.Context, provider, emoteID string) error
	MarkFailed(ctx context.Context, provider, emoteID, reason string, maxAttempts int) error
	RequeueProcessing(ctx context.Context) (int, error)
	Enqueue(ctx context.Context, provider string, emoteIDs []string, priority int, force bool) (int, error)
	UpsertMetadata(ctx context.Context, e *Emote) error
	GetEmote(ctx context.Context, provider, emoteID string) (*Emote, error)
	SaveClassification(ctx context.Context, provider, emoteID string, c *Classification, model string, version int) error
	SaveFlashMeasure(ctx context.Context, provider, emoteID string, m FlashMeasure) error
	SaveEmbeddings(ctx context.Context, provider, emoteID string, imageEmbedding, descEmbedding []float32) error
}

// Worker drains the classification queue as fast as the shared GPU allows. The
// vision model runs on the same server as live roleplay, so its call waits for
// an idle one; every other stage is arranged not to make it wait twice.
type Worker struct {
	logger     *slog.Logger
	store      workerStore
	source     EmoteSource
	classifier *Classifier
	embedder   Embedder
	objects    ObjectStore
	popular    seventv.PopularSource
	gate       *GPUGate

	maxAttempts int
	seedCount   int
	seedQueries []SeedSource

	processed      atomic.Int64
	reusedVerdicts atomic.Int64
}

func NewWorker(logger *slog.Logger, store *Store, source EmoteSource, popular seventv.PopularSource, classifier *Classifier, embedder Embedder, objects ObjectStore, cfg *Config) *Worker {
	return &Worker{
		logger:      logger,
		store:       store,
		source:      source,
		popular:     popular,
		classifier:  classifier,
		embedder:    embedder,
		objects:     objects,
		gate:        NewGPUGate(logger.With("component", "gpu-gate"), cfg.VisionSlotsURL, cfg.ClassifyInterval),
		maxAttempts: cfg.MaxAttempts,
		seedCount:   cfg.SeedCount,
		seedQueries: cfg.SeedQueries,
	}
}

// Pipeline shape. The vision call is the only stage that cannot be run
// concurrently — it shares its GPU with live roleplay — so everything else is
// arranged around keeping exactly one of them in flight and never making it
// wait: one emote is prepared ahead of the model, and the embeddings of finished
// emotes are batched behind it.
const (
	// Preparing more than one emote ahead buys nothing, since vision is the slow
	// stage, and costs another emote's work thrown away on shutdown.
	prepQueueDepth = 1
	judgedDepth    = 32
	// Description embeddings have a fixed per-call cost of about 0.85 s and about
	// 0.02 s per item, so one at a time is nearly all overhead: measured 0.95 s
	// alone against 0.075 s each at sixteen. The embedding server shares the GPU
	// with the vision model and with live roleplay, so this is mostly about how
	// much of that GPU a sweep takes, not about the sweep's own wall clock.
	descBatchSize = 16
	// A partial batch is embedded anyway after this long. It has to be longer than
	// the model takes per emote, or the timer fires between two emotes and every
	// batch flushes at one item — which is exactly the cost batching removes. It
	// is a safety net for a queue that has run dry, not the normal path.
	descBatchWait = 30 * time.Second
	// The last batch is worth landing even as the service goes down: its emotes
	// are otherwise reprocessed on the next start.
	shutdownFlushGrace = 10 * time.Second
	// drainLogEvery paces the summary line. Reused verdicts are the number to
	// watch after a resweep: one that reuses everything judged nothing, however
	// cleanly its queue drained.
	drainLogEvery = 100
)

// preparedEmote is one emote past every stage that does not need the GPU: its
// metadata upserted, its original and grid in S3, its frames coalesced and
// measured for flashing. The vision loop takes it ready to go, so the model's
// turn is spent on the model.
type preparedEmote struct {
	provider string
	id       string
	name     string
	grid     *Grid
	cached   *Emote

	// force means this emote was queued to be judged again rather than caught
	// up, so the stored verdict must not be reused however current it looks.
	force bool

	classification *Classification
	imageEmbedding []float32
	descEmbedding  []float32

	claimed  time.Time
	prepared time.Duration
	judged   time.Duration
}

// Run drains the queue until the context ends, one stage per goroutine. A
// failing emote never stops the pipeline: it is logged, recorded against the
// queue row, and skipped.
func (w *Worker) Run(ctx context.Context) error {
	if n, err := w.store.RequeueProcessing(ctx); err != nil {
		w.logger.Error("failed to requeue claimed emotes", "err", err)
	} else if n > 0 {
		w.logger.Info("requeued emotes claimed by a previous run", "count", n)
	}

	if w.embedder == nil {
		w.logger.Warn("embedder not configured, emotes will be classified without rule-matching vectors")
	}

	group, ctx := errgroup.WithContext(ctx)
	prepared := make(chan *preparedEmote, prepQueueDepth)
	judged := make(chan *preparedEmote, judgedDepth)

	group.Go(func() error {
		defer close(prepared)
		return w.prepLoop(ctx, prepared)
	})
	group.Go(func() error {
		defer close(judged)
		return w.visionLoop(ctx, prepared, judged)
	})
	group.Go(func() error {
		return w.finishLoop(ctx, judged)
	})

	return group.Wait()
}

// prepLoop claims emotes and does everything the GPU is not needed for.
func (w *Worker) prepLoop(ctx context.Context, out chan<- *preparedEmote) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		provider, emoteID, force, err := w.store.ClaimNext(ctx)
		if errors.Is(err, ErrQueueEmpty) {
			if err := sleepCtx(ctx, emptyQueuePoll); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			w.logger.Error("failed to claim emote", "err", err)
			if err := sleepCtx(ctx, emptyQueuePoll); err != nil {
				return err
			}
			continue
		}

		started := time.Now()
		item, err := w.prepare(ctx, provider, emoteID)
		if err != nil {
			w.failed(ctx, provider, emoteID, "prepare", err)
			continue
		}
		item.force = force
		item.claimed = started
		item.prepared = time.Since(started)

		select {
		case out <- item:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// visionLoop is the only place a vision request is made, which is what holds the
// one-in-flight invariant: the classifier's two-pass fluids check runs inside
// this stage as a second sequential call, under the same gated turn.
func (w *Worker) visionLoop(ctx context.Context, in <-chan *preparedEmote, out chan<- *preparedEmote) error {
	for item := range in {
		started := time.Now()
		if err := w.judge(ctx, item); err != nil {
			w.failed(ctx, item.provider, item.id, "classify", err)
			continue
		}
		item.judged = time.Since(started)

		select {
		case out <- item:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

// finishLoop batches description embeddings behind the model. Emotes stay
// claimed until their batch lands, so an interrupted run requeues them rather
// than leaving a row marked done without its vector.
func (w *Worker) finishLoop(ctx context.Context, in <-chan *preparedEmote) error {
	batch := make([]*preparedEmote, 0, descBatchSize)
	ticker := time.NewTicker(descBatchWait)
	defer ticker.Stop()

	for {
		select {
		case item, ok := <-in:
			if !ok {
				// The upstream stages close this channel when the service is going
				// down, so ctx is already cancelled and the writes need one of their
				// own.
				final, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownFlushGrace)
				w.flush(final, batch)
				cancel()
				return nil
			}
			batch = append(batch, item)
			if len(batch) >= descBatchSize {
				w.flush(ctx, batch)
				batch = batch[:0]
			}
		case <-ticker.C:
			w.flush(ctx, batch)
			batch = batch[:0]
		}
	}
}

// flush embeds a batch and marks its emotes done. A batch that fails to embed is
// failed whole: every emote in it is requeued, and the retry costs no vision
// call because the verdict it already wrote is reused.
func (w *Worker) flush(ctx context.Context, batch []*preparedEmote) {
	if len(batch) == 0 {
		return
	}

	if err := w.embedBatch(ctx, batch); err != nil {
		for _, item := range batch {
			w.failed(ctx, item.provider, item.id, "embed", err)
		}
		return
	}

	for _, item := range batch {
		if err := w.store.MarkDone(ctx, item.provider, item.id); err != nil {
			w.logger.Error("failed to mark emote done", "emote_id", item.id, "err", err)
			continue
		}
		w.logger.Info("emote processed",
			"provider", item.provider, "emote_id", item.id,
			"took", time.Since(item.claimed), "prep", item.prepared, "vision", item.judged)

		if done := w.processed.Add(1); done%drainLogEvery == 0 {
			w.logger.Info("drain progress",
				"processed", done, "verdicts_reused", w.reusedVerdicts.Load())
		}
	}
}

// failed records a per-emote failure without stopping the stage that hit it.
func (w *Worker) failed(ctx context.Context, provider, emoteID, stage string, cause error) {
	w.logger.Error("emote processing failed",
		"provider", provider, "emote_id", emoteID, "stage", stage, "err", cause)
	if err := w.store.MarkFailed(ctx, provider, emoteID, cause.Error(), w.maxAttempts); err != nil {
		w.logger.Error("failed to record processing failure", "emote_id", emoteID, "err", err)
	}
}

// ProcessEmote runs every stage for one emote inline. It is the pipeline's body
// without the pipeline, for callers that want one emote handled now.
func (w *Worker) ProcessEmote(ctx context.Context, provider, emoteID string) error {
	item, err := w.prepare(ctx, provider, emoteID)
	if err != nil {
		return err
	}
	if err := w.judge(ctx, item); err != nil {
		return err
	}
	return w.embedBatch(ctx, []*preparedEmote{item})
}

// prepare fetches metadata, stores the original and the grid, and measures
// flashing — every stage that needs no GPU.
func (w *Worker) prepare(ctx context.Context, provider, emoteID string) (*preparedEmote, error) {
	if provider = NormalizeProvider(provider); provider != ProviderSevenTV {
		return nil, fmt.Errorf("no client for emote provider %q", provider)
	}

	meta, err := w.source.GetEmote(ctx, emoteID)
	if err != nil {
		return nil, fmt.Errorf("fetch metadata: %w", err)
	}

	emote := metadataToEmote(meta)
	if err := w.store.UpsertMetadata(ctx, emote); err != nil {
		return nil, err
	}

	webp, err := w.source.DownloadImage(ctx, emoteID, seventv.File4x)
	if err != nil {
		return nil, fmt.Errorf("download image: %w", err)
	}

	// 7TV's own frame count decides whether there is anything to sample: for a
	// static emote 4x.webp is already the single frame, and ffprobe cannot count
	// frames in an animated webp at all.
	var grid *Grid
	if meta.FrameCount(seventv.File4x) > 1 {
		grid, err = w.classifier.BuildGrid(ctx, webp)
	} else {
		grid, err = w.classifier.BuildStatic(ctx, webp)
	}
	if err != nil {
		return nil, fmt.Errorf("build grid: %w", err)
	}

	if w.objects != nil {
		emote.OriginalKey = "original/" + emoteID + ".webp"
		if err := w.objects.PutObject(ctx, s3client.EmotesBucket, emote.OriginalKey,
			bytes.NewReader(webp), int64(len(webp)), "image/webp"); err != nil {
			return nil, fmt.Errorf("store original: %w", err)
		}
		emote.GridKey = "grid/" + emoteID + ".png"
		if err := w.objects.PutObject(ctx, s3client.EmotesBucket, emote.GridKey,
			bytes.NewReader(grid.PNG), int64(len(grid.PNG)), "image/png"); err != nil {
			return nil, fmt.Errorf("store grid: %w", err)
		}
		if err := w.store.UpsertMetadata(ctx, emote); err != nil {
			return nil, err
		}
	}

	cached, err := w.store.GetEmote(ctx, provider, emoteID)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return nil, err
	}

	return &preparedEmote{provider: provider, id: emoteID, name: meta.Name, grid: grid, cached: cached}, nil
}

// judge takes the emote's turn with the vision model and writes what it decided.
func (w *Worker) judge(ctx context.Context, item *preparedEmote) error {
	classification, err := w.classify(ctx, item.provider, item.id, item.name, item.grid, item.cached, item.force)
	if err != nil {
		return err
	}
	item.classification = classification

	// After the verdict, so it wins: the measure owns the flashing class whether
	// the vision call ran or its cached answer was reused.
	return w.store.SaveFlashMeasure(ctx, item.provider, item.id, item.grid.Flash)
}

// embedBatch attaches the vectors rules are matched against. Descriptions go in
// one call for the whole batch; an image embedding is per emote and only for one
// that has never had it, since the grid is rebuilt from the same stored original
// and a vector that already exists is the vector this pass would produce.
//
// That reuse is deliberate and force does not override it: force means judge
// this emote again, and the image vector does not depend on the judgement. It
// would only go stale if the grid recipe itself changed — the frame count or the
// montage layout — and nothing today can request those vectors again.
func (w *Worker) embedBatch(ctx context.Context, batch []*preparedEmote) error {
	if w.embedder == nil {
		return nil
	}

	for _, item := range batch {
		if item.cached != nil && item.cached.HasImageEmbedding {
			continue
		}
		vector, err := w.embedImage(ctx, item.grid.PNG)
		if err != nil {
			return err
		}
		item.imageEmbedding = vector
	}

	if err := w.embedDescriptions(ctx, batch); err != nil {
		return err
	}

	for _, item := range batch {
		if err := w.store.SaveEmbeddings(ctx, item.provider, item.id, item.imageEmbedding, item.descEmbedding); err != nil {
			return err
		}
	}
	return nil
}

func (w *Worker) embedDescriptions(ctx context.Context, batch []*preparedEmote) error {
	texts := make([]string, 0, len(batch))
	described := make([]*preparedEmote, 0, len(batch))
	for _, item := range batch {
		if item.classification == nil || item.classification.Description == "" {
			continue
		}
		texts = append(texts, item.classification.Description)
		described = append(described, item)
	}
	if len(texts) == 0 {
		return nil
	}

	vectors, err := w.embedder.EmbedText(ctx, texts)
	if err != nil {
		return fmt.Errorf("embed descriptions: %w", err)
	}
	if len(vectors) != len(texts) {
		return fmt.Errorf("embed descriptions: got %d vectors for %d texts", len(vectors), len(texts))
	}

	for i, item := range described {
		item.descEmbedding = vectors[i]
	}
	return nil
}

// classify reuses a verdict only when it came from the current model *and* the
// current taxonomy, and only when the emote was not queued for re-judgement.
// The vision call is the expensive step, so a retry from further down the
// pipeline must not pay for it twice — but a stale row must never be mistaken
// for a fresh one, and neither must a row someone asked to have judged again:
// a prompt change that deliberately holds the version steady leaves every row
// looking current, so without force the reuse would swallow the whole resweep.
func (w *Worker) classify(ctx context.Context, provider, emoteID, name string, grid *Grid, cached *Emote, force bool) (*Classification, error) {
	if !force && cached.Classified() && cached.Model == w.classifier.Model() && !cached.Stale(ClassifierVersion) {
		w.reusedVerdicts.Add(1)
		return &Classification{Description: cached.Description, Classes: cached.Classes}, nil
	}

	classification, err := w.visionClassify(ctx, name, grid)
	if err != nil {
		return nil, err
	}
	if err := w.store.SaveClassification(ctx, provider, emoteID, classification, w.classifier.Model(), ClassifierVersion); err != nil {
		return nil, err
	}
	return classification, nil
}

// visionClassify waits for an idle GPU and only then sends the request. The
// order is load-bearing: our own request occupies a slot, so gating after
// dispatch would wait on ourselves forever.
func (w *Worker) visionClassify(ctx context.Context, name string, grid *Grid) (*Classification, error) {
	if w.gate != nil {
		if err := w.gate.Wait(ctx); err != nil {
			return nil, err
		}
	}
	return w.classifier.Classify(ctx, name, grid)
}

// reflashPause paces the backfill. It is thousands of emotes of local CPU with
// nothing waiting on it, so it runs one at a time and idles between them rather
// than taking the box for half an hour.
const reflashPause = 200 * time.Millisecond

// ErrObjectStoreUnavailable is returned when a job needs S3 and none is configured.
var ErrObjectStoreUnavailable = errors.New("emoteservice: no object store configured")

// ReflashStats is what one backfill run did.
type ReflashStats struct {
	Emotes   int `json:"emotes"`
	Measured int `json:"measured"`
	Failed   int `json:"failed"`
}

// Reflash rescores emotes for flashing from their stored originals. It touches
// nothing the vision model produced — no description, no other class, no
// embedding, no classifier_version — so it can run against a registry whose
// taxonomy is stale, and it needs no GPU: the vision endpoint is shared with
// live roleplay.
func (w *Worker) Reflash(ctx context.Context, targets []FlashTarget) (ReflashStats, error) {
	if w.objects == nil {
		return ReflashStats{}, ErrObjectStoreUnavailable
	}

	stats := ReflashStats{Emotes: len(targets)}
	for i, target := range targets {
		if err := ctx.Err(); err != nil {
			return stats, err
		}

		if err := w.reflashOne(ctx, target); err != nil {
			stats.Failed++
			w.logger.Error("failed to re-measure flashing",
				"provider", target.Provider, "emote_id", target.EmoteID, "err", err)
		} else {
			stats.Measured++
		}

		if (i+1)%100 == 0 {
			w.logger.Info("flash backfill progress", "done", i+1, "of", len(targets), "failed", stats.Failed)
		}
		if err := sleepCtx(ctx, reflashPause); err != nil {
			return stats, err
		}
	}
	return stats, nil
}

func (w *Worker) reflashOne(ctx context.Context, target FlashTarget) error {
	object, err := w.objects.GetObject(ctx, s3client.EmotesBucket, target.OriginalKey)
	if err != nil {
		return fmt.Errorf("fetch original: %w", err)
	}
	webp, err := io.ReadAll(object)
	object.Close()
	if err != nil {
		return fmt.Errorf("read original: %w", err)
	}

	flash, err := w.classifier.MeasureWebP(ctx, webp)
	if err != nil {
		return err
	}
	return w.store.SaveFlashMeasure(ctx, target.Provider, target.EmoteID, flash)
}

func metadataToEmote(meta *seventv.Emote) *Emote {
	return &Emote{
		Provider: ProviderSevenTV,
		ID:       meta.ID,
		Name:     meta.Name,
		Listed:   meta.Listed,
		Animated: meta.Animated,
		Deleted:  meta.Deleted,
		Flags:    meta.Flags,
		Tags:     meta.Tags,
	}
}

// DefaultSeedSources is what a seed request that names nothing runs: the global
// head plus every configured query.
func (w *Worker) DefaultSeedSources() []SeedSource {
	sources := make([]SeedSource, 0, len(w.seedQueries)+1)
	sources = append(sources, SeedSource{Count: w.seedCount})
	return append(sources, w.seedQueries...)
}

// globalSeedQuery labels the global set in the crawl log. It is not a 7TV query:
// the global set is fetched by id, and no query could reach it.
const globalSeedQuery = "__global__"

// Seed crawls the 7TV global set and each configured source, records the
// metadata up front so the registry is browsable before the GPU gets to it, and
// enqueues the union. Sources overlap heavily, so emotes are deduplicated
// across them.
//
// Crawling is fast; draining is not: the queue only advances while the shared
// GPU is idle, so a large seed drains over however much quiet time the streamer
// leaves it.
func (w *Worker) Seed(ctx context.Context, sources []SeedSource, priority int) (int, error) {
	if w.popular == nil || w.source == nil {
		return 0, ErrSeedingUnavailable
	}
	if len(sources) == 0 {
		sources = w.DefaultSeedSources()
	}

	emotes, err := w.crawlSeedSources(ctx, sources)
	if err != nil {
		return 0, err
	}

	ids := make([]string, 0, len(emotes))
	for i := range emotes {
		if err := w.store.UpsertMetadata(ctx, metadataToEmote(&emotes[i])); err != nil {
			return 0, err
		}
		ids = append(ids, emotes[i].ID)
	}

	queued, err := w.store.Enqueue(ctx, ProviderSevenTV, ids, priority, false)
	if err != nil {
		return 0, err
	}
	w.logger.Info("seeded classification queue", "sources", len(sources), "unique", len(ids), "queued", queued)
	return queued, nil
}

// crawlSeedSources gathers the global set and every configured source into one
// deduplicated list, in crawl order.
//
// The global set is not a source a caller can configure away. It renders in
// every channel, which makes it the set most worth classifying, but it is
// curated rather than popularity-ranked: nobody channel-adds an emote that is
// already everywhere, so its members sit at near-zero all-time use and no top-N
// depth ever reaches them. Crawling it first also means a 7TV outage on the one
// set that matters everywhere fails the run before it spends eight requests on
// the popularity pages.
func (w *Worker) crawlSeedSources(ctx context.Context, sources []SeedSource) ([]seventv.Emote, error) {
	seen := make(map[string]struct{})
	out := make([]seventv.Emote, 0)

	collect := func(query string, emotes []seventv.Emote) {
		fresh := 0
		for _, meta := range emotes {
			if _, dup := seen[meta.ID]; dup {
				continue
			}
			seen[meta.ID] = struct{}{}
			out = append(out, meta)
			fresh++
		}
		w.logger.Info("crawled seed source", "query", query, "fetched", len(emotes), "new", fresh)
	}

	global, err := w.source.GetGlobalSet(ctx)
	if err != nil {
		return nil, fmt.Errorf("fetch the 7TV global set: %w", err)
	}
	collect(globalSeedQuery, globalSetMetadata(global))

	for _, source := range sources {
		count := source.Count
		if count <= 0 {
			count = w.seedCount
		}

		emotes, err := w.popular.TopEmotes(ctx, source.Query, count)
		if err != nil {
			return nil, fmt.Errorf("fetch top emotes for %q: %w", source.Query, err)
		}
		collect(source.Query, emotes)
	}

	return out, nil
}

// globalSetMetadata unwraps a set's entries into emote metadata. The id comes
// from the entry rather than from the emote it carries, since the entry is what
// every other moderation key is taken from.
func globalSetMetadata(set *seventv.EmoteSet) []seventv.Emote {
	out := make([]seventv.Emote, 0, len(set.Emotes))
	for _, entry := range set.Emotes {
		meta := entry.Emote
		meta.ID = entry.EmoteID
		out = append(out, meta)
	}
	return out
}

func (w *Worker) embedImage(ctx context.Context, grid []byte) ([]float32, error) {
	images, err := w.embedder.EmbedPNGs(ctx, [][]byte{grid})
	if err != nil {
		return nil, fmt.Errorf("embed grid: %w", err)
	}
	if len(images) != 1 {
		return nil, fmt.Errorf("embed grid: got %d vectors", len(images))
	}
	return images[0], nil
}

func (w *Worker) embedText(ctx context.Context, description string) ([]float32, error) {
	if description == "" {
		return nil, nil
	}
	texts, err := w.embedder.EmbedText(ctx, []string{description})
	if err != nil {
		return nil, fmt.Errorf("embed description: %w", err)
	}
	if len(texts) != 1 {
		return nil, fmt.Errorf("embed description: got %d vectors", len(texts))
	}
	return texts[0], nil
}
