package history

import (
	"encoding/json"
	"testing"
	"time"

	"app/db"
	"app/pkg/archive"
	"app/pkg/textfilter"

	"github.com/google/uuid"
)

func exportRow(t *testing.T, arch *archive.MessageArchive, data *db.MessageData) db.ExportRow {
	t.Helper()
	id, err := uuid.NewV7()
	if err != nil {
		t.Fatal(err)
	}
	var archJSON, dataJSON []byte
	if arch != nil {
		archJSON, _ = json.Marshal(arch)
	}
	if data != nil {
		dataJSON, _ = json.Marshal(data)
	}
	rt := 1
	card := uuid.New()
	return db.ExportRow{
		ID: id, UserID: uuid.New(), ChannelLogin: "streamer", Status: db.MsgStatusProcessed, Updated: 42,
		Msg:  db.TwitchMessage{TwitchLogin: "viewer", TwitchUserID: 7, Message: "hello", RewardID: "rw"},
		Data: dataJSON, Archive: archJSON, Owned: true, RewardType: &rt, CardID: &card, CardName: "Doctor",
	}
}

func TestFlattenRollupsAndChildren(t *testing.T) {
	arch := &archive.MessageArchive{
		Handler: "AI", Outcome: archive.OutcomePlayed,
		StartedAt: 1_700_000_000_000, FinishedAt: 1_700_000_012_000,
		LLMCalls: []archive.LLMCall{
			{Kind: "character", Model: "m1", PromptTok: 100, OutputTok: 20, LatencyMs: 900, Request: json.RawMessage(`{"x":1}`)},
			{Kind: "filter", Model: "m2", PromptTok: 50, OutputTok: 5, LatencyMs: 300},
		},
		TTSTracks: []archive.TTSTrack{
			{Engine: "index", AudioSec: 4.5, PlayedSec: 4.5, AudioKey: "c/m/t1.mp3"},
			{Engine: "index", AudioSec: 6, PlayedSec: 2, Cut: archive.CutSkip},
		},
		Filter: []archive.FilterRun{
			{Target: archive.FilterTargetRequest, LLM: []textfilter.Span{{Start: 0, End: 2}}, LatencyMs: 300},
			{Target: archive.FilterTargetReply, Regex: []textfilter.Span{{Start: 1, End: 3}}, Skipped: true},
		},
	}
	row := exportRow(t, arch, &db.MessageData{AIResponse: "reply", ImageIDs: []string{"img"}})
	f := Flatten(row)
	m := f.Message

	if m.ChannelID != row.UserID || m.ChannelLogin != "streamer" || m.TwitchLogin != "viewer" || m.TwitchUserID != 7 {
		t.Errorf("identity columns wrong: %+v", m)
	}
	if m.RewardType != 1 || m.CardName != "Doctor" || m.CardID == nil || *m.CardID != *row.CardID {
		t.Errorf("reward columns wrong: %+v", m)
	}
	if m.Handler != "AI" || m.Outcome != "played" || m.Status != int8(db.MsgStatusProcessed) || m.Updated != 42 {
		t.Errorf("state columns wrong: %+v", m)
	}
	if m.StartedAt == nil || m.FinishedAt == nil || m.FinishedAt.Sub(*m.StartedAt) != 12*time.Second {
		t.Errorf("timestamps wrong: %v %v", m.StartedAt, m.FinishedAt)
	}
	if m.LLMCalls != 2 || m.PromptTokens != 150 || m.CompletionTokens != 25 {
		t.Errorf("llm rollup wrong: %+v", m)
	}
	if m.TTSTracks != 2 || m.AudioSec != 10.5 || m.PlayedSec != 6.5 {
		t.Errorf("tts rollup wrong: %+v", m)
	}
	if m.FilterLLMHits != 1 || m.FilterRegexHits != 1 {
		t.Errorf("filter rollup wrong: %+v", m)
	}
	if m.AIResponse != "reply" || len(m.ImageIDs) != 1 || m.Archive == "" || m.Data == "" {
		t.Errorf("payload columns wrong: %+v", m)
	}

	if len(f.LLM) != 2 || f.LLM[1].Idx != 1 || f.LLM[0].Request != `{"x":1}` || f.LLM[0].MsgID != row.ID {
		t.Errorf("llm rows wrong: %+v", f.LLM)
	}
	if len(f.Tracks) != 2 || f.Tracks[1].Cut != "skip" || f.Tracks[0].AudioKey != "c/m/t1.mp3" {
		t.Errorf("track rows wrong: %+v", f.Tracks)
	}
	if len(f.Filters) != 2 || f.Filters[0].LLMHits != 1 || !f.Filters[1].Skipped || f.Filters[1].RegexSpans != `[{"start":1,"end":3}]` {
		t.Errorf("filter rows wrong: %+v", f.Filters)
	}
	// children take the message's start time when they carry none of their own
	if !f.Tracks[0].At.Equal(*m.StartedAt) {
		t.Errorf("track at = %v, want %v", f.Tracks[0].At, *m.StartedAt)
	}
}

func TestFlattenWithoutArchive(t *testing.T) {
	row := exportRow(t, nil, nil)
	row.RewardType = nil
	row.CardID = nil
	row.Status = db.MsgStatusWait
	f := Flatten(row)
	if f.Message.RewardType != -1 || f.Message.Handler != "" || f.Message.Outcome != "" || f.Message.Archive != "" {
		t.Errorf("waiting row flattened wrong: %+v", f.Message)
	}
	if f.Message.ImageIDs == nil {
		t.Error("image_ids must be an empty array, not null")
	}
	if len(f.LLM)+len(f.Tracks)+len(f.Filters) != 0 {
		t.Error("no archive must mean no child rows")
	}
	if f.Message.EnqueuedAt.IsZero() {
		t.Error("enqueued_at must come from the uuidv7")
	}
}

func TestToItem(t *testing.T) {
	arch := &archive.MessageArchive{
		Handler: "AI", Outcome: archive.OutcomePlayed, StartedAt: 1000, FinishedAt: 4000,
		LLMCalls:  []archive.LLMCall{{Kind: "character", Model: "m1", PromptTok: 10, OutputTok: 2, LatencyMs: 500, Request: json.RawMessage(`{}`), Response: "r"}},
		TTSTracks: []archive.TTSTrack{{Engine: "index", AudioSec: 3, PlayedSec: 3, AudioKey: "chan/msg/0f2d6a3e-0000-4000-8000-000000000001.mp3"}},
		Turns:     []archive.AgenticTurn{{Speaker: "Doc", Text: "hi"}},
	}
	row := exportRow(t, arch, &db.MessageData{AIResponse: "reply", RequestFiltered: []textfilter.Span{{Start: 0, End: 1}}, ImageIDs: []string{"img1"}})
	m := Flatten(row).Message

	viewer := ToItem(m, ItemOptions{
		AudioURL: func(c, msg, track string) string { return "/a/" + c + "/" + msg + "/" + track },
		ImageURL: func(id string) string { return "/i/" + id },
	})
	if viewer.Type != "AI" || viewer.RequestedBy != "viewer" || viewer.Status != "Processed" || viewer.DurationMs != 3000 {
		t.Errorf("item header wrong: %+v", viewer)
	}
	if len(viewer.RequestSpans) != 1 || viewer.Response != "reply" || len(viewer.ImageURLs) != 1 || viewer.ImageURLs[0] != "/i/img1" {
		t.Errorf("item body wrong: %+v", viewer)
	}
	want := "/a/" + row.UserID.String() + "/" + row.ID.String() + "/0f2d6a3e-0000-4000-8000-000000000001"
	if len(viewer.Tracks) != 1 || viewer.Tracks[0].AudioURL != want {
		t.Errorf("audio url = %q, want %q", viewer.Tracks[0].AudioURL, want)
	}
	if viewer.LLM == nil || viewer.LLM.Calls != 1 || viewer.LLM.PromptTokens != 10 || len(viewer.LLM.Models) != 1 {
		t.Errorf("llm summary wrong: %+v", viewer.LLM)
	}
	if len(viewer.LLMCalls) != 0 {
		t.Error("raw llm calls must not reach non-admin items")
	}
	if len(viewer.Turns) != 1 || viewer.Turns[0].Speaker != "Doc" {
		t.Errorf("turns wrong: %+v", viewer.Turns)
	}

	admin := ToItem(m, ItemOptions{WithLLMCalls: true})
	if len(admin.LLMCalls) != 1 || admin.LLMCalls[0].Response != "r" {
		t.Errorf("admin item lacks raw llm calls: %+v", admin.LLMCalls)
	}
	if len(admin.Tracks) != 1 || admin.Tracks[0].AudioURL != "" {
		t.Error("no AudioURL builder must mean no audio url")
	}

	chat := m
	chat.RewardID = ""
	if got := ToItem(chat, ItemOptions{}).Type; got != "Chat TTS" {
		t.Errorf("chat type = %q", got)
	}
}
