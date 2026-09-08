// Package archive is the per-message telemetry collector behind
// adr/history-archive.md: handlers, LLM clients and TTS players record what a
// redeem produced, and the processor writes the result once into
// msg_queue.archive. Recording is infallible and nil-safe: without a collector
// in the context every call is a no-op.
package archive

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"app/pkg/ffmpeg"
	"app/pkg/textfilter"

	"github.com/google/uuid"
)

const (
	OutcomePlayed      = "played"
	OutcomeSkipped     = "skipped"
	OutcomeError       = "error"
	OutcomeBulkSkipped = "bulk_skipped"
	OutcomeAborted     = "aborted"
	// OutcomeSilent: the handler finished cleanly but produced no track (empty
	// filtered text, no characters detected, engine failure it logged itself).
	OutcomeSilent = "silent"

	FilterTargetRequest = "request"
	FilterTargetReply   = "reply"
	// FilterTargetRequester: spans over the requester's login as spoken in the
	// "<login> asked me:" lead-in.
	FilterTargetRequester = "requester"

	CutSkip     = "skip"
	CutTTSLimit = "tts_limit"
	CutError    = "error"
	CutAborted  = "aborted"
)

// MessageArchive is the msg_queue.archive document: one per message, written
// once when its handler returns.
type MessageArchive struct {
	RewardType *int    `json:"reward_type,omitempty"`
	CardID     *string `json:"card_id,omitempty"`
	Handler    string  `json:"handler,omitempty"`
	StartedAt  int64   `json:"started_at,omitempty"`
	FinishedAt int64   `json:"finished_at,omitempty"`
	Outcome    string  `json:"outcome,omitempty"`
	Error      string  `json:"error,omitempty"`

	LLMCalls  []LLMCall     `json:"llm_calls,omitempty"`
	TTSTracks []TTSTrack    `json:"tts_tracks,omitempty"`
	Filter    []FilterRun   `json:"filter_runs,omitempty"`
	Turns     []AgenticTurn `json:"turns,omitempty"`
}

// LLMCall is one request to a language model. Request is the exact body sent
// with image bytes replaced by "image:<id>" references; Response is the raw
// content before any thinking/whitespace cleanup.
type LLMCall struct {
	Kind      string          `json:"kind"`
	Model     string          `json:"model"`
	Endpoint  string          `json:"endpoint"`
	Request   json.RawMessage `json:"request"`
	Response  string          `json:"response"`
	PromptTok int             `json:"prompt_tokens,omitempty"`
	OutputTok int             `json:"completion_tokens,omitempty"`
	LatencyMs int             `json:"latency_ms"`
	Error     string          `json:"error,omitempty"`
	At        int64           `json:"at"`
}

// TTSTrack is one continuous audio timeline as the overlay saw it. AudioKey is
// the tts-archive object holding the streamed MP3 bytes; empty when the upload
// failed or was never attempted.
type TTSTrack struct {
	Engine       string                `json:"engine"`
	Text         string                `json:"text"`
	VoiceSHA     string                `json:"voice_sha,omitempty"`
	AudioKey     string                `json:"audio_key,omitempty"`
	AudioSec     float64               `json:"audio_sec"`
	Chunks       int                   `json:"chunks"`
	FirstChunkMs int                   `json:"first_chunk_ms"`
	SynthMs      int                   `json:"synth_ms"`
	PlayedSec    float64               `json:"played_sec"`
	Cut          string                `json:"cut,omitempty"`
	Loudness     *ffmpeg.LoudnessStats `json:"loudness,omitempty"`
}

type FilterRun struct {
	Target    string            `json:"target"`
	Regex     []textfilter.Span `json:"regex,omitempty"`
	LLM       []textfilter.Span `json:"llm,omitempty"`
	LatencyMs int               `json:"latency_ms"`
	Skipped   bool              `json:"skipped,omitempty"`
}

func (r FilterRun) Spans() []textfilter.Span {
	return textfilter.Merge(r.Regex, r.LLM)
}

type AgenticTurn struct {
	CardID  string `json:"card_id"`
	Speaker string `json:"speaker"`
	Text    string `json:"text"`
}

// Collector accumulates one message's archive. All methods are safe on a nil
// receiver and from concurrent goroutines.
type Collector struct {
	channelID uuid.UUID
	msgID     uuid.UUID

	mu sync.Mutex
	a  MessageArchive

	pending sync.WaitGroup
}

func NewCollector(channelID, msgID uuid.UUID) *Collector {
	return &Collector{
		channelID: channelID,
		msgID:     msgID,
		a:         MessageArchive{StartedAt: time.Now().UnixMilli()},
	}
}

type ctxKey struct{}
type kindKey struct{}

func WithCollector(ctx context.Context, c *Collector) context.Context {
	return context.WithValue(ctx, ctxKey{}, c)
}

// FromContext returns the collector for the message being processed, or nil.
func FromContext(ctx context.Context) *Collector {
	c, _ := ctx.Value(ctxKey{}).(*Collector)
	return c
}

// WithLLMKind labels every LLM call made under ctx ("character", "filter",
// ...). The clients record calls without knowing why they are made.
func WithLLMKind(ctx context.Context, kind string) context.Context {
	return context.WithValue(ctx, kindKey{}, kind)
}

func llmKind(ctx context.Context) string {
	k, _ := ctx.Value(kindKey{}).(string)
	return k
}

func RecordLLM(ctx context.Context, call LLMCall) {
	c := FromContext(ctx)
	if c == nil {
		return
	}
	if call.Kind == "" {
		call.Kind = llmKind(ctx)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.a.LLMCalls = append(c.a.LLMCalls, call)
}

func RecordFilter(ctx context.Context, run FilterRun) {
	c := FromContext(ctx)
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.a.Filter = append(c.a.Filter, run)
}

func RecordTurn(ctx context.Context, turn AgenticTurn) {
	c := FromContext(ctx)
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.a.Turns = append(c.a.Turns, turn)
}

func (c *Collector) SetHandler(handler string, rewardType *int, cardID *uuid.UUID) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.a.Handler = handler
	c.a.RewardType = rewardType
	if cardID != nil {
		s := cardID.String()
		c.a.CardID = &s
	}
}

// RecordTrack appends a track and returns its index for SetAudioKey.
func (c *Collector) RecordTrack(t TTSTrack) int {
	if c == nil {
		return -1
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.a.TTSTracks = append(c.a.TTSTracks, t)
	return len(c.a.TTSTracks) - 1
}

func (c *Collector) SetAudioKey(idx int, key string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if idx >= 0 && idx < len(c.a.TTSTracks) {
		c.a.TTSTracks[idx].AudioKey = key
	}
}

// AudioKey is the tts-archive object name for a track of this message.
func (c *Collector) AudioKey(trackID uuid.UUID) string {
	if c == nil {
		return ""
	}
	return c.channelID.String() + "/" + c.msgID.String() + "/" + trackID.String() + ".mp3"
}

// Go runs fn in a goroutine that Wait accounts for: a player still finishing
// after a skip, or an audio upload started after playback, still lands in the
// archive before it is written. Without a collector fn simply runs.
func (c *Collector) Go(fn func()) {
	if c == nil {
		go fn()
		return
	}
	c.pending.Add(1)
	go func() {
		defer c.pending.Done()
		fn()
	}()
}

// Wait blocks for pending work up to timeout; false means some is still running.
func (c *Collector) Wait(timeout time.Duration) bool {
	if c == nil {
		return true
	}
	done := make(chan struct{})
	go func() {
		c.pending.Wait()
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-time.After(timeout):
		return false
	}
}

// Finish stamps the outcome and returns the archive to store.
func (c *Collector) Finish(outcome string, err error) *MessageArchive {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.a.FinishedAt = time.Now().UnixMilli()
	c.a.Outcome = outcome
	if outcome == OutcomePlayed && len(c.a.TTSTracks) == 0 {
		c.a.Outcome = OutcomeSilent
	}
	if err != nil {
		c.a.Error = err.Error()
	}
	// a copy with its own slices: an upload that outlived Wait must not write
	// into the document while it is being marshalled
	out := c.a
	out.LLMCalls = append([]LLMCall(nil), c.a.LLMCalls...)
	out.TTSTracks = append([]TTSTrack(nil), c.a.TTSTracks...)
	out.Filter = append([]FilterRun(nil), c.a.Filter...)
	out.Turns = append([]AgenticTurn(nil), c.a.Turns...)
	return &out
}
