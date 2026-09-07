package history

import (
	"encoding/json"
	"path"
	"strings"

	"app/db"
	"app/pkg/archive"
	"app/pkg/textfilter"
)

// Item is a history entry as the panel and admin views render it: the
// message with its filter highlights, what played, and per-message rollups.
// LLMCalls is filled only for admins (WithLLMCalls).
type Item struct {
	ID          string `json:"id"`
	EnqueuedAt  int64  `json:"enqueued_at"`
	Channel     string `json:"channel"`
	RequestedBy string `json:"requested_by"`
	Type        string `json:"type"`
	CharName    string `json:"char_name,omitempty"`
	Status      string `json:"status"`
	Outcome     string `json:"outcome,omitempty"`
	Error       string `json:"error,omitempty"`

	Request      string            `json:"request"`
	Response     string            `json:"response,omitempty"`
	RequestSpans []textfilter.Span `json:"request_spans,omitempty"`
	ReplySpans   []textfilter.Span `json:"reply_spans,omitempty"`
	ImageURLs    []string          `json:"image_urls,omitempty"`

	DurationMs int64         `json:"duration_ms,omitempty"`
	Tracks     []ItemTrack   `json:"tracks,omitempty"`
	LLM        *ItemLLM      `json:"llm,omitempty"`
	Filter     *ItemFilter   `json:"filter,omitempty"`
	Turns      []ItemTurn    `json:"turns,omitempty"`
	LLMCalls   []ItemLLMCall `json:"llm_calls,omitempty"`
}

type ItemTrack struct {
	Engine       string  `json:"engine"`
	Text         string  `json:"text"`
	AudioURL     string  `json:"audio_url,omitempty"`
	AudioSec     float64 `json:"audio_sec"`
	PlayedSec    float64 `json:"played_sec"`
	Cut          string  `json:"cut,omitempty"`
	FirstChunkMs int     `json:"first_chunk_ms"`
	SynthMs      int     `json:"synth_ms"`
}

type ItemLLM struct {
	Calls            int      `json:"calls"`
	PromptTokens     int      `json:"prompt_tokens"`
	CompletionTokens int      `json:"completion_tokens"`
	LatencyMs        int      `json:"latency_ms"`
	Models           []string `json:"models"`
}

type ItemFilter struct {
	Runs      int  `json:"runs"`
	LLMHits   int  `json:"llm_hits"`
	RegexHits int  `json:"regex_hits"`
	LatencyMs int  `json:"latency_ms"`
	LLMUsed   bool `json:"llm_used"`
}

type ItemTurn struct {
	Speaker string `json:"speaker"`
	Text    string `json:"text"`
}

type ItemLLMCall struct {
	Kind             string          `json:"kind"`
	Model            string          `json:"model"`
	Endpoint         string          `json:"endpoint"`
	Request          json.RawMessage `json:"request"`
	Response         string          `json:"response"`
	PromptTokens     int             `json:"prompt_tokens"`
	CompletionTokens int             `json:"completion_tokens"`
	LatencyMs        int             `json:"latency_ms"`
	Error            string          `json:"error,omitempty"`
}

// ItemOptions shapes the view: AudioURL builds the proxied link for a track
// (channel id, message id, track id); ImageURL the same for a user image.
type ItemOptions struct {
	AudioURL     func(channelID, msgID, trackID string) string
	ImageURL     func(imageID string) string
	WithLLMCalls bool
}

func ToItem(m MessageRow, opts ItemOptions) Item {
	it := Item{
		ID:          m.ID.String(),
		EnqueuedAt:  m.EnqueuedAt.UnixMilli(),
		Channel:     m.ChannelLogin,
		RequestedBy: m.TwitchLogin,
		Type:        typeLabel(m),
		CharName:    m.CardName,
		Status:      db.MsgStatus(m.Status).String(),
		Outcome:     m.Outcome,
		Error:       m.Error,
		Request:     m.Message,
		Response:    m.AIResponse,
	}
	_ = json.Unmarshal([]byte(m.RequestSpans), &it.RequestSpans)
	_ = json.Unmarshal([]byte(m.ReplySpans), &it.ReplySpans)
	if opts.ImageURL != nil {
		for _, id := range m.ImageIDs {
			it.ImageURLs = append(it.ImageURLs, opts.ImageURL(id))
		}
	}
	if m.StartedAt != nil && m.FinishedAt != nil {
		it.DurationMs = m.FinishedAt.Sub(*m.StartedAt).Milliseconds()
	}

	if m.Archive == "" {
		return it
	}
	var arch archive.MessageArchive
	if err := json.Unmarshal([]byte(m.Archive), &arch); err != nil {
		return it
	}

	for _, t := range arch.TTSTracks {
		track := ItemTrack{
			Engine: t.Engine, Text: t.Text, AudioSec: t.AudioSec, PlayedSec: t.PlayedSec, Cut: t.Cut,
			FirstChunkMs: t.FirstChunkMs, SynthMs: t.SynthMs,
		}
		if t.AudioKey != "" && opts.AudioURL != nil {
			trackID := strings.TrimSuffix(path.Base(t.AudioKey), ".mp3")
			track.AudioURL = opts.AudioURL(m.ChannelID.String(), m.ID.String(), trackID)
		}
		it.Tracks = append(it.Tracks, track)
	}

	if len(arch.LLMCalls) > 0 {
		llm := &ItemLLM{Calls: len(arch.LLMCalls)}
		seen := map[string]bool{}
		for _, c := range arch.LLMCalls {
			llm.PromptTokens += c.PromptTok
			llm.CompletionTokens += c.OutputTok
			llm.LatencyMs += c.LatencyMs
			if !seen[c.Model] {
				seen[c.Model] = true
				llm.Models = append(llm.Models, c.Model)
			}
			if opts.WithLLMCalls {
				it.LLMCalls = append(it.LLMCalls, ItemLLMCall{
					Kind: c.Kind, Model: c.Model, Endpoint: c.Endpoint, Request: c.Request, Response: c.Response,
					PromptTokens: c.PromptTok, CompletionTokens: c.OutputTok, LatencyMs: c.LatencyMs, Error: c.Error,
				})
			}
		}
		it.LLM = llm
	}

	if len(arch.Filter) > 0 {
		f := &ItemFilter{Runs: len(arch.Filter)}
		for _, r := range arch.Filter {
			f.LLMHits += len(r.LLM)
			f.RegexHits += len(r.Regex)
			f.LatencyMs += r.LatencyMs
			if !r.Skipped {
				f.LLMUsed = true
			}
		}
		it.Filter = f
	}

	for _, t := range arch.Turns {
		it.Turns = append(it.Turns, ItemTurn{Speaker: t.Speaker, Text: t.Text})
	}

	return it
}

func typeLabel(m MessageRow) string {
	if m.RewardID == "" {
		return "Chat TTS"
	}
	if m.RewardType < 0 {
		return "unknown"
	}
	return db.TwitchRewardType(m.RewardType).String()
}
