package api

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"app/db"
	"app/pkg/ai"
	"app/pkg/ffmpeg"
	"app/pkg/whisperx"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

const voiceSampleText = "oh my god, thank you so much"

// filterSampleVoice is the voice used to demo TTS filters on the voices page.
const filterSampleVoice = "les"

// VoiceSampler synthesizes speech and applies TTS filters to audio.
type VoiceSampler interface {
	TTSWithTimings(ctx context.Context, msg string, refAudio []byte) ([]byte, []whisperx.Timiing, error)
	ChatTTSWithTimings(ctx context.Context, msg string, refAudio []byte) ([]byte, []whisperx.Timiing, error)
	ApplyFilters(ctx context.Context, audio []byte, filters ...string) ([]byte, error)
}

// VoiceReferenceProvider resolves a public short voice name to its card data.
type VoiceReferenceProvider interface {
	GetVoiceReferenceByShortName(ctx context.Context, shortName string) (uuid.UUID, *db.CardData, error)
}

// VoiceSampleCache generates per-voice sample audio and keeps it in memory
// forever. Concurrent requests for the same voice synthesize only once: the
// first caller generates while the rest wait for its result.
type VoiceSampleCache struct {
	sampler  VoiceSampler
	provider VoiceReferenceProvider

	mu       sync.Mutex
	samples  map[string][]byte
	inflight map[string]chan struct{}
}

func NewVoiceSampleCache(sampler VoiceSampler, provider VoiceReferenceProvider) *VoiceSampleCache {
	return &VoiceSampleCache{
		sampler:  sampler,
		provider: provider,
		samples:  make(map[string][]byte),
		inflight: make(map[string]chan struct{}),
	}
}

func (c *VoiceSampleCache) getOrGenerate(ctx context.Context, key string, gen func(ctx context.Context) ([]byte, error)) ([]byte, error) {
	for {
		c.mu.Lock()
		if audio, ok := c.samples[key]; ok {
			c.mu.Unlock()
			return audio, nil
		}

		if ch, ok := c.inflight[key]; ok {
			c.mu.Unlock()
			select {
			case <-ch:
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}

		ch := make(chan struct{})
		c.inflight[key] = ch
		c.mu.Unlock()

		// detach from the requester so closing the tab mid-generation doesn't
		// abort the work other waiters are counting on
		genCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
		audio, err := gen(genCtx)
		cancel()

		c.mu.Lock()
		delete(c.inflight, key)
		if err == nil {
			c.samples[key] = audio
		}
		c.mu.Unlock()
		close(ch)

		return audio, err
	}
}

func (c *VoiceSampleCache) Get(ctx context.Context, voice string) ([]byte, error) {
	return c.getOrGenerate(ctx, "voice:"+voice, func(ctx context.Context) ([]byte, error) {
		_, card, err := c.provider.GetVoiceReferenceByShortName(ctx, voice)
		if err != nil {
			return nil, fmt.Errorf("failed to get voice reference: %w", err)
		}

		if len(card.VoiceReference) == 0 {
			return nil, fmt.Errorf("voice '%s' has no voice reference", voice)
		}

		audio, _, err := c.sampler.TTSWithTimings(ctx, voiceSampleText, card.VoiceReference)
		if err != nil {
			return nil, fmt.Errorf("failed to synthesize sample: %w", err)
		}

		return audio, nil
	})
}

// GetEmotion synthesizes the sample text with an emotion vector and caches it
// per emotion. Unlike filters this is a full TTS pass, so the result is cached.
func (c *VoiceSampleCache) GetEmotion(ctx context.Context, voice, emotion string) ([]byte, error) {
	return c.getOrGenerate(ctx, "emotion:"+voice+":"+emotion, func(ctx context.Context) ([]byte, error) {
		_, card, err := c.provider.GetVoiceReferenceByShortName(ctx, voice)
		if err != nil {
			return nil, fmt.Errorf("failed to get voice reference: %w", err)
		}

		if len(card.VoiceReference) == 0 {
			return nil, fmt.Errorf("voice '%s' has no voice reference", voice)
		}

		audio, _, err := c.sampler.TTSWithTimings(ctx, ai.InsertEmotions(voiceSampleText, []string{emotion}), card.VoiceReference)
		if err != nil {
			return nil, fmt.Errorf("failed to synthesize emotion sample: %w", err)
		}

		return audio, nil
	})
}

// GetOld synthesizes the sample text with the old StyleTTS2 engine ({old} filter).
func (c *VoiceSampleCache) GetOld(ctx context.Context, voice string) ([]byte, error) {
	return c.getOrGenerate(ctx, "old:"+voice, func(ctx context.Context) ([]byte, error) {
		_, card, err := c.provider.GetVoiceReferenceByShortName(ctx, voice)
		if err != nil {
			return nil, fmt.Errorf("failed to get voice reference: %w", err)
		}

		if len(card.VoiceReference) == 0 {
			return nil, fmt.Errorf("voice '%s' has no voice reference", voice)
		}

		audio, _, err := c.sampler.ChatTTSWithTimings(ctx, voiceSampleText, card.VoiceReference)
		if err != nil {
			return nil, fmt.Errorf("failed to synthesize old tts sample: %w", err)
		}

		return audio, nil
	})
}

// GetFiltered applies a TTS filter to the cached voice sample. Only the base
// TTS synthesis is cached — the ffmpeg filter pass is cheap and runs per request.
func (c *VoiceSampleCache) GetFiltered(ctx context.Context, voice string, filterID int) ([]byte, error) {
	base, err := c.Get(ctx, voice)
	if err != nil {
		return nil, err
	}

	audio, err := c.sampler.ApplyFilters(ctx, base, strconv.Itoa(filterID))
	if err != nil {
		return nil, fmt.Errorf("failed to apply filter %d: %w", filterID, err)
	}

	return audio, nil
}

type filterItem struct {
	ID   int
	Name string
}

// filterItems lists all TTS filters with their names, sorted by ID.
var filterItems []filterItem

func init() {
	for t := ffmpeg.FilterType(1); t < ffmpeg.FilterLast; t++ {
		filterItems = append(filterItems, filterItem{ID: int(t), Name: t.Name()})
	}

	sort.Slice(filterItems, func(i, j int) bool { return filterItems[i].ID < filterItems[j].ID })
}

func (api *API) voiceSample(w http.ResponseWriter, r *http.Request) {
	voice := strings.ToLower(strings.TrimSpace(chi.URLParam(r, "voice")))
	if voice == "" {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("empty voice"))
		return
	}

	audio, err := api.voiceSamples.Get(r.Context(), voice)
	if err != nil {
		api.logger.Error("failed to get voice sample", "voice", voice, "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("failed to generate sample"))
		return
	}

	w.Header().Set("Content-Type", "audio/wav")
	_, _ = w.Write(audio)
}

func (api *API) emotionSample(w http.ResponseWriter, r *http.Request) {
	name := strings.ToLower(strings.TrimSpace(chi.URLParam(r, "name")))
	if !ai.IsEmotion(name) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("invalid emotion"))
		return
	}

	audio, err := api.voiceSamples.GetEmotion(r.Context(), filterSampleVoice, name)
	if err != nil {
		api.logger.Error("failed to get emotion sample", "emotion", name, "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("failed to generate sample"))
		return
	}

	w.Header().Set("Content-Type", "audio/wav")
	_, _ = w.Write(audio)
}

func (api *API) oldSample(w http.ResponseWriter, r *http.Request) {
	audio, err := api.voiceSamples.GetOld(r.Context(), filterSampleVoice)
	if err != nil {
		api.logger.Error("failed to get old tts sample", "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("failed to generate sample"))
		return
	}

	w.Header().Set("Content-Type", "audio/wav")
	_, _ = w.Write(audio)
}

func (api *API) filterSample(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil || id < 1 || id >= int(ffmpeg.FilterLast) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("invalid filter id"))
		return
	}

	audio, err := api.voiceSamples.GetFiltered(r.Context(), filterSampleVoice, id)
	if err != nil {
		api.logger.Error("failed to get filter sample", "filter", id, "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("failed to generate sample"))
		return
	}

	w.Header().Set("Content-Type", "audio/wav")
	_, _ = w.Write(audio)
}
