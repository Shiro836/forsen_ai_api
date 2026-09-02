package emoteservice

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"app/pkg/oai"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeStore records what the pipeline wrote, in order.
type fakeStore struct {
	mu    sync.Mutex
	calls []string
}

func (f *fakeStore) record(format string, args ...any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fmt.Sprintf(format, args...))
}

func (f *fakeStore) recorded() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fakeStore) ClaimNext(context.Context) (string, string, bool, error) {
	return "", "", false, ErrQueueEmpty
}
func (f *fakeStore) RequeueProcessing(context.Context) (int, error) { return 0, nil }
func (f *fakeStore) Enqueue(context.Context, string, []string, int, bool) (int, error) {
	return 0, nil
}
func (f *fakeStore) UpsertMetadata(context.Context, *Emote) error { return nil }
func (f *fakeStore) GetEmote(context.Context, string, string) (*Emote, error) {
	return nil, ErrNotFound
}

func (f *fakeStore) MarkDone(_ context.Context, _, emoteID string) error {
	f.record("done %s", emoteID)
	return nil
}

func (f *fakeStore) MarkFailed(_ context.Context, _, emoteID, reason string, _ int) error {
	f.record("failed %s %s", emoteID, reason)
	return nil
}

func (f *fakeStore) SaveClassification(_ context.Context, _, emoteID string, _ *Classification, _ string, _ int) error {
	f.record("classification %s", emoteID)
	return nil
}

func (f *fakeStore) SaveFlashMeasure(_ context.Context, _, emoteID string, _ FlashMeasure) error {
	f.record("flash %s", emoteID)
	return nil
}

func (f *fakeStore) SaveEmbeddings(_ context.Context, _, emoteID string, _, desc []float32) error {
	f.record("embeddings %s len=%d", emoteID, len(desc))
	return nil
}

// countingVision fails the test if two vision requests are ever in flight at
// once, which is the invariant the whole pipeline is shaped around.
type countingVision struct {
	t *testing.T

	mu       sync.Mutex
	inFlight int
	peak     int
	calls    int
}

func (v *countingVision) AskVisionJSON(_ context.Context, prompt string, _ []oai.Image, _ json.RawMessage, _ float64) (string, error) {
	v.mu.Lock()
	v.inFlight++
	v.calls++
	if v.inFlight > v.peak {
		v.peak = v.inFlight
	}
	peak := v.peak
	v.mu.Unlock()

	require.Equal(v.t, 1, peak, "a second vision request went out while one was in flight")
	time.Sleep(2 * time.Millisecond)

	v.mu.Lock()
	v.inFlight--
	v.mu.Unlock()

	if strings.Contains(prompt, `"liquid"`) {
		return `{"liquid": "tears"}`, nil
	}
	return `{"description": "a king crying blue tears", "classes": ["fluids"]}`, nil
}

type recordingEmbedder struct {
	mu     sync.Mutex
	texts  [][]string
	images int
}

func (e *recordingEmbedder) EmbedText(_ context.Context, texts []string) ([][]float32, error) {
	e.mu.Lock()
	e.texts = append(e.texts, append([]string(nil), texts...))
	e.mu.Unlock()

	out := make([][]float32, len(texts))
	for i := range out {
		out[i] = []float32{float32(i)}
	}
	return out, nil
}

func (e *recordingEmbedder) EmbedPNGs(_ context.Context, images [][]byte) ([][]float32, error) {
	e.mu.Lock()
	e.images += len(images)
	e.mu.Unlock()

	out := make([][]float32, len(images))
	for i := range out {
		out[i] = []float32{1}
	}
	return out, nil
}

func (e *recordingEmbedder) batches() [][]string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([][]string(nil), e.texts...)
}

func pipelineWorker(t *testing.T, store workerStore, vision VisionModel, embedder Embedder) *Worker {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return &Worker{
		logger:     logger,
		store:      store,
		classifier: &Classifier{logger: logger, vision: vision},
		embedder:   embedder,
	}
}

func preparedItems(n int) []*preparedEmote {
	out := make([]*preparedEmote, 0, n)
	for i := range n {
		out = append(out, &preparedEmote{
			provider: ProviderSevenTV,
			id:       fmt.Sprintf("emote%02d", i),
			name:     "OHNYOO",
			grid:     &Grid{PNG: []byte{1}, Tiles: 10},
			cached:   &Emote{HasImageEmbedding: true},
		})
	}
	return out
}

// The fluids check makes a classification two calls; both belong to the same
// turn at the model, not to two overlapping ones.
func TestVisionLoopKeepsOneCallInFlight(t *testing.T) {
	vision := &countingVision{t: t}
	worker := pipelineWorker(t, &fakeStore{}, vision, nil)

	in := make(chan *preparedEmote, 8)
	out := make(chan *preparedEmote, 8)
	for _, item := range preparedItems(8) {
		in <- item
	}
	close(in)

	require.NoError(t, worker.visionLoop(context.Background(), in, out))
	close(out)

	judged := 0
	for item := range out {
		require.NotNil(t, item.classification)
		judged++
	}
	assert.Equal(t, 8, judged)
	assert.Equal(t, 1, vision.peak)
	assert.Equal(t, 16, vision.calls, "each emote takes the taxonomy call and the tears check")
}

func TestFinishLoopBatchesDescriptions(t *testing.T) {
	store := &fakeStore{}
	embedder := &recordingEmbedder{}
	worker := pipelineWorker(t, store, nil, embedder)

	in := make(chan *preparedEmote, descBatchSize)
	for _, item := range preparedItems(descBatchSize) {
		item.classification = &Classification{Description: "a description of " + item.id}
		in <- item
	}
	close(in)

	require.NoError(t, worker.finishLoop(context.Background(), in))

	batches := embedder.batches()
	require.Len(t, batches, 1, "a full batch is one call, not one per emote")
	assert.Len(t, batches[0], descBatchSize)
	assert.Zero(t, embedder.images, "an emote that already has an image vector is not re-embedded")

	done := 0
	for _, call := range store.recorded() {
		if strings.HasPrefix(call, "done ") {
			done++
		}
	}
	assert.Equal(t, descBatchSize, done)
}

// A row marked done without its vector would never be looked at again, so the
// batch has to land first.
func TestFinishLoopMarksDoneAfterEmbeddings(t *testing.T) {
	store := &fakeStore{}
	worker := pipelineWorker(t, store, nil, &recordingEmbedder{})

	in := make(chan *preparedEmote, 2)
	for _, item := range preparedItems(2) {
		item.classification = &Classification{Description: "a description"}
		in <- item
	}
	close(in)

	require.NoError(t, worker.finishLoop(context.Background(), in))

	calls := store.recorded()
	for i, call := range calls {
		if strings.HasPrefix(call, "done ") {
			id := strings.TrimPrefix(call, "done ")
			assert.Contains(t, calls[:i], "embeddings "+id+" len=1", "%s was marked done before its embeddings landed", id)
		}
	}
}

// An emote with no description still has to finish: the batch skips it rather
// than sending an empty string to the embedder.
func TestFinishLoopSkipsEmptyDescriptions(t *testing.T) {
	store := &fakeStore{}
	embedder := &recordingEmbedder{}
	worker := pipelineWorker(t, store, nil, embedder)

	items := preparedItems(3)
	items[0].classification = &Classification{Description: "described"}
	items[1].classification = &Classification{Description: ""}
	items[2].classification = &Classification{Description: "also described"}

	in := make(chan *preparedEmote, 3)
	for _, item := range items {
		in <- item
	}
	close(in)

	require.NoError(t, worker.finishLoop(context.Background(), in))

	batches := embedder.batches()
	require.Len(t, batches, 1)
	assert.Equal(t, []string{"described", "also described"}, batches[0])
	assert.Contains(t, store.recorded(), "embeddings emote01 len=0")
}

// storedVerdict is a row that looks entirely current: same model, same taxonomy
// version, judged. It is the state every row was in during the resweep that
// drained without judging anything.
func storedVerdict() *Emote {
	judged := time.Now()
	return &Emote{
		Description:       "the stored description",
		Classes:           []string{ClassFluids},
		ClassifierVersion: ClassifierVersion,
		ClassifiedAt:      &judged,
		HasImageEmbedding: true,
	}
}

// Reuse is what keeps a retry from paying for the vision call twice.
func TestJudgeReusesAStoredVerdict(t *testing.T) {
	vision := &countingVision{t: t}
	worker := pipelineWorker(t, &fakeStore{}, vision, nil)

	item := preparedItems(1)[0]
	item.cached = storedVerdict()

	require.NoError(t, worker.judge(context.Background(), item))

	assert.Zero(t, vision.calls, "a current verdict is not worth a vision call")
	assert.Equal(t, "the stored description", item.classification.Description)
	assert.Equal(t, int64(1), worker.reusedVerdicts.Load())
}

// ...and force is what stops that reuse from swallowing a resweep whole. This is
// the bug: the queue drained clean, every row looked current, and nothing was
// ever sent to the model.
func TestJudgeIgnoresAStoredVerdictWhenForced(t *testing.T) {
	vision := &countingVision{t: t}
	store := &fakeStore{}
	worker := pipelineWorker(t, store, vision, nil)

	item := preparedItems(1)[0]
	item.cached = storedVerdict()
	item.force = true

	require.NoError(t, worker.judge(context.Background(), item))

	assert.Positive(t, vision.calls, "a forced emote must reach the model")
	assert.NotEqual(t, "the stored description", item.classification.Description)
	assert.Zero(t, worker.reusedVerdicts.Load())
	assert.Contains(t, store.recorded(), "classification emote00", "the fresh verdict must be written")
}
