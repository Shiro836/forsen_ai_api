// Package history is the archive side of adr/history-archive.md: it flattens
// queue rows into the ClickHouse tables, runs the exporter that feeds them,
// and reads them back for the control panel merged with the not-yet-exported
// postgres tail.
package history

import (
	"encoding/json"
	"time"

	"app/db"
	"app/pkg/archive"
	"app/pkg/textfilter"
	"app/pkg/tools"

	"github.com/google/uuid"
)

// MessageRow is one version of a message in the messages table.
type MessageRow struct {
	ID               uuid.UUID  `ch:"id"`
	Updated          uint64     `ch:"updated"`
	ChannelID        uuid.UUID  `ch:"channel_id"`
	ChannelLogin     string     `ch:"channel_login"`
	TwitchUserID     uint64     `ch:"twitch_user_id"`
	TwitchLogin      string     `ch:"twitch_login"`
	RewardID         string     `ch:"reward_id"`
	RewardType       int8       `ch:"reward_type"`
	CardID           *uuid.UUID `ch:"card_id"`
	CardName         string     `ch:"card_name"`
	Handler          string     `ch:"handler"`
	Status           int8       `ch:"status"`
	Outcome          string     `ch:"outcome"`
	Message          string     `ch:"message"`
	AIResponse       string     `ch:"ai_response"`
	RequestSpans     string     `ch:"request_spans"`
	ReplySpans       string     `ch:"reply_spans"`
	ImageIDs         []string   `ch:"image_ids"`
	ShowImages       *bool      `ch:"show_images"`
	EnqueuedAt       time.Time  `ch:"enqueued_at"`
	StartedAt        *time.Time `ch:"started_at"`
	FinishedAt       *time.Time `ch:"finished_at"`
	Error            string     `ch:"error"`
	LLMCalls         uint16     `ch:"llm_calls"`
	PromptTokens     uint32     `ch:"prompt_tokens"`
	CompletionTokens uint32     `ch:"completion_tokens"`
	TTSTracks        uint16     `ch:"tts_tracks"`
	AudioSec         float32    `ch:"audio_sec"`
	PlayedSec        float32    `ch:"played_sec"`
	FilterLLMHits    uint16     `ch:"filter_llm_hits"`
	FilterRegexHits  uint16     `ch:"filter_regex_hits"`
	Data             string     `ch:"data"`
	Archive          string     `ch:"archive"`
}

type LLMCallRow struct {
	MsgID            uuid.UUID `ch:"msg_id"`
	ChannelID        uuid.UUID `ch:"channel_id"`
	Idx              uint16    `ch:"idx"`
	Updated          uint64    `ch:"updated"`
	At               time.Time `ch:"at"`
	Kind             string    `ch:"kind"`
	Model            string    `ch:"model"`
	Endpoint         string    `ch:"endpoint"`
	Request          string    `ch:"request"`
	Response         string    `ch:"response"`
	PromptTokens     uint32    `ch:"prompt_tokens"`
	CompletionTokens uint32    `ch:"completion_tokens"`
	LatencyMs        uint32    `ch:"latency_ms"`
	Error            string    `ch:"error"`
}

type TTSTrackRow struct {
	MsgID        uuid.UUID `ch:"msg_id"`
	ChannelID    uuid.UUID `ch:"channel_id"`
	Idx          uint16    `ch:"idx"`
	Updated      uint64    `ch:"updated"`
	At           time.Time `ch:"at"`
	Engine       string    `ch:"engine"`
	Text         string    `ch:"text"`
	VoiceSHA     string    `ch:"voice_sha"`
	AudioKey     string    `ch:"audio_key"`
	AudioSec     float32   `ch:"audio_sec"`
	PlayedSec    float32   `ch:"played_sec"`
	Chunks       uint16    `ch:"chunks"`
	FirstChunkMs uint32    `ch:"first_chunk_ms"`
	SynthMs      uint32    `ch:"synth_ms"`
	Cut          string    `ch:"cut"`
}

type FilterRunRow struct {
	MsgID      uuid.UUID `ch:"msg_id"`
	ChannelID  uuid.UUID `ch:"channel_id"`
	Idx        uint16    `ch:"idx"`
	Updated    uint64    `ch:"updated"`
	At         time.Time `ch:"at"`
	Target     string    `ch:"target"`
	RegexSpans string    `ch:"regex_spans"`
	LLMSpans   string    `ch:"llm_spans"`
	RegexHits  uint16    `ch:"regex_hits"`
	LLMHits    uint16    `ch:"llm_hits"`
	LatencyMs  uint32    `ch:"latency_ms"`
	Skipped    bool      `ch:"skipped"`
}

// Flat is one queue row spread over the archive tables.
type Flat struct {
	Message MessageRow
	LLM     []LLMCallRow
	Tracks  []TTSTrackRow
	Filters []FilterRunRow
}

// Flatten builds the archive rows for one version of a queue row. The
// archive document is parsed for the rollups and child rows and kept
// verbatim in Message.Archive for columns nobody has invented yet.
func Flatten(r db.ExportRow) Flat {
	msgData, err := db.ParseMessageData(r.Data)
	if err != nil {
		msgData = &db.MessageData{}
	}

	var arch archive.MessageArchive
	hasArchive := len(r.Archive) > 0 && string(r.Archive) != "null"
	if hasArchive {
		if err := json.Unmarshal(r.Archive, &arch); err != nil {
			hasArchive = false
		}
	}

	m := MessageRow{
		ID:           r.ID,
		Updated:      uint64(r.Updated),
		ChannelID:    r.UserID,
		ChannelLogin: r.ChannelLogin,
		TwitchUserID: uint64(r.Msg.TwitchUserID),
		TwitchLogin:  r.Msg.TwitchLogin,
		RewardID:     r.Msg.RewardID,
		RewardType:   -1,
		CardID:       r.CardID,
		CardName:     r.CardName,
		Status:       int8(r.Status),
		Message:      r.Msg.Message,
		AIResponse:   msgData.AIResponse,
		RequestSpans: spansJSON(msgData.RequestFiltered),
		ReplySpans:   spansJSON(msgData.FilteredText),
		ImageIDs:     msgData.ImageIDs,
		ShowImages:   msgData.ShowImages,
		EnqueuedAt:   tools.UUIDToTime(r.ID),
		Data:         string(r.Data),
	}
	if m.ImageIDs == nil {
		m.ImageIDs = []string{}
	}
	if r.RewardType != nil {
		m.RewardType = int8(*r.RewardType)
	}
	if !hasArchive {
		return Flat{Message: m}
	}

	m.Archive = string(r.Archive)
	m.Handler = arch.Handler
	m.Outcome = arch.Outcome
	m.Error = arch.Error
	if arch.StartedAt != 0 {
		t := time.UnixMilli(arch.StartedAt).UTC()
		m.StartedAt = &t
	}
	if arch.FinishedAt != 0 {
		t := time.UnixMilli(arch.FinishedAt).UTC()
		m.FinishedAt = &t
	}

	at := m.EnqueuedAt
	if m.StartedAt != nil {
		at = *m.StartedAt
	}

	f := Flat{Message: m}
	for i, c := range arch.LLMCalls {
		callAt := at
		if c.At != 0 {
			callAt = time.UnixMilli(c.At).UTC()
		}
		f.LLM = append(f.LLM, LLMCallRow{
			MsgID: r.ID, ChannelID: r.UserID, Idx: uint16(i), Updated: m.Updated, At: callAt,
			Kind: c.Kind, Model: c.Model, Endpoint: c.Endpoint,
			Request: string(c.Request), Response: c.Response,
			PromptTokens: uint32(c.PromptTok), CompletionTokens: uint32(c.OutputTok),
			LatencyMs: uint32(c.LatencyMs), Error: c.Error,
		})
		f.Message.PromptTokens += uint32(c.PromptTok)
		f.Message.CompletionTokens += uint32(c.OutputTok)
	}
	f.Message.LLMCalls = uint16(len(arch.LLMCalls))

	for i, t := range arch.TTSTracks {
		f.Tracks = append(f.Tracks, TTSTrackRow{
			MsgID: r.ID, ChannelID: r.UserID, Idx: uint16(i), Updated: m.Updated, At: at,
			Engine: t.Engine, Text: t.Text, VoiceSHA: t.VoiceSHA, AudioKey: t.AudioKey,
			AudioSec: float32(t.AudioSec), PlayedSec: float32(t.PlayedSec), Chunks: uint16(t.Chunks),
			FirstChunkMs: uint32(t.FirstChunkMs), SynthMs: uint32(t.SynthMs), Cut: t.Cut,
		})
		f.Message.AudioSec += float32(t.AudioSec)
		f.Message.PlayedSec += float32(t.PlayedSec)
	}
	f.Message.TTSTracks = uint16(len(arch.TTSTracks))

	for i, fr := range arch.Filter {
		f.Filters = append(f.Filters, FilterRunRow{
			MsgID: r.ID, ChannelID: r.UserID, Idx: uint16(i), Updated: m.Updated, At: at,
			Target: fr.Target, RegexSpans: spansJSON(fr.Regex), LLMSpans: spansJSON(fr.LLM),
			RegexHits: uint16(len(fr.Regex)), LLMHits: uint16(len(fr.LLM)),
			LatencyMs: uint32(fr.LatencyMs), Skipped: fr.Skipped,
		})
		f.Message.FilterLLMHits += uint16(len(fr.LLM))
		f.Message.FilterRegexHits += uint16(len(fr.Regex))
	}

	return f
}

func spansJSON(spans []textfilter.Span) string {
	if len(spans) == 0 {
		return "[]"
	}
	b, err := json.Marshal(spans)
	if err != nil {
		return "[]"
	}
	return string(b)
}
