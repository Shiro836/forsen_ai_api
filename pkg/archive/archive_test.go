package archive

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestNoCollectorIsNoop(t *testing.T) {
	ctx := context.Background()
	RecordLLM(ctx, LLMCall{Kind: "x"})
	RecordFilter(ctx, FilterRun{})
	RecordTurn(ctx, AgenticTurn{})

	var c *Collector
	if idx := c.RecordTrack(TTSTrack{}); idx != -1 {
		t.Fatalf("nil collector returned track index %d", idx)
	}
	c.SetAudioKey(0, "k")
	if !c.Wait(time.Millisecond) {
		t.Fatal("nil collector should have nothing to wait for")
	}
	if c.Finish(OutcomePlayed, nil) != nil {
		t.Fatal("nil collector should finish to nil")
	}

	ran := make(chan struct{})
	c.Go(func() { close(ran) })
	select {
	case <-ran:
	case <-time.After(time.Second):
		t.Fatal("Go on a nil collector must still run fn")
	}
}

func TestKindFromContext(t *testing.T) {
	col := NewCollector(uuid.New(), uuid.New())
	ctx := WithCollector(context.Background(), col)

	RecordLLM(WithLLMKind(ctx, "filter"), LLMCall{Model: "m"})
	RecordLLM(WithLLMKind(ctx, "filter"), LLMCall{Kind: "explicit"})
	RecordLLM(ctx, LLMCall{})

	a := col.Finish(OutcomePlayed, nil)
	got := []string{a.LLMCalls[0].Kind, a.LLMCalls[1].Kind, a.LLMCalls[2].Kind}
	want := []string{"filter", "explicit", ""}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("call %d kind = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestAudioKeyLandsBeforeFinish(t *testing.T) {
	channel, msg, track := uuid.New(), uuid.New(), uuid.New()
	col := NewCollector(channel, msg)

	idx := col.RecordTrack(TTSTrack{Engine: "index"})
	key := col.AudioKey(track)
	if want := channel.String() + "/" + msg.String() + "/" + track.String() + ".mp3"; key != want {
		t.Fatalf("key = %q, want %q", key, want)
	}

	release := make(chan struct{})
	col.Go(func() {
		<-release
		col.SetAudioKey(idx, key)
	})

	if col.Wait(10 * time.Millisecond) {
		t.Fatal("Wait returned before the upload finished")
	}
	close(release)
	if !col.Wait(time.Second) {
		t.Fatal("Wait timed out after the upload finished")
	}

	a := col.Finish(OutcomePlayed, nil)
	if a.TTSTracks[0].AudioKey != key {
		t.Fatalf("audio key = %q, want %q", a.TTSTracks[0].AudioKey, key)
	}
}

func TestFinishOutcome(t *testing.T) {
	col := NewCollector(uuid.New(), uuid.New())
	a := col.Finish(OutcomePlayed, nil)
	if a.Outcome != OutcomeSilent {
		t.Errorf("no tracks: outcome = %q, want %q", a.Outcome, OutcomeSilent)
	}
	if a.StartedAt == 0 || a.FinishedAt < a.StartedAt {
		t.Errorf("timestamps not set: started %d finished %d", a.StartedAt, a.FinishedAt)
	}

	col = NewCollector(uuid.New(), uuid.New())
	col.RecordTrack(TTSTrack{})
	if a := col.Finish(OutcomePlayed, nil); a.Outcome != OutcomePlayed {
		t.Errorf("with a track: outcome = %q, want %q", a.Outcome, OutcomePlayed)
	}

	col = NewCollector(uuid.New(), uuid.New())
	if a := col.Finish(OutcomeError, errors.New("boom")); a.Error != "boom" || a.Outcome != OutcomeError {
		t.Errorf("error outcome = %+v", a)
	}

	// Finish returns a snapshot: later records do not alias into it
	col = NewCollector(uuid.New(), uuid.New())
	col.RecordTrack(TTSTrack{Engine: "a"})
	a = col.Finish(OutcomePlayed, nil)
	col.RecordTrack(TTSTrack{Engine: "b"})
	if len(a.TTSTracks) != 1 {
		t.Errorf("snapshot grew to %d tracks", len(a.TTSTracks))
	}
}
