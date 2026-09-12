package api

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"app/internal/app/history"
	"app/pkg/textfilter"
)

func TestHighlightSpans(t *testing.T) {
	got := string(highlightSpans("a <b> ñc", []textfilter.Span{{Start: 2, End: 5}, {Start: 6, End: 99}}))
	want := `a <span class="rounded px-0.5 bg-red-500/30">&lt;b&gt;</span> <span class="rounded px-0.5 bg-red-500/30">ñc</span>`
	if got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
	if got := string(highlightSpans("<x>", nil)); got != "&lt;x&gt;" {
		t.Fatalf("unhighlighted text not escaped: %q", got)
	}
}

// The highlight class is written in a Go string literal, so it reaches the
// stylesheet only while tailwind.config.js scans .go and the css is rebuilt;
// otherwise the spans render colorless.
func TestHighlightClassIsCompiled(t *testing.T) {
	css, err := os.ReadFile("static/tailwind.css")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(css), `bg-red-500\/30`) {
		t.Fatal(`bg-red-500\/30 missing from static/tailwind.css; run scripts/tailwind.sh`)
	}
}

func TestHistoryTemplatesRender(t *testing.T) {
	now := time.Now()
	items := []history.Item{{
		ID: "m1", EnqueuedAt: now.Add(-90 * time.Second).UnixMilli(), Channel: "chan", RequestedBy: "viewer",
		Type: "AI", CharName: "Forsen", Status: "Processed", Outcome: "played",
		Request: "hello <world>", RequestSpans: []textfilter.Span{{Start: 6, End: 13}},
		Response: "reply", ImageURLs: []string{"/images/1?w=512"}, DurationMs: 4200,
		Tracks:   []history.ItemTrack{{Engine: "indextts", AudioURL: "/archive-audio/a/b/c", AudioSec: 3.14, PlayedSec: 1, Cut: "skip"}},
		LLM:      &history.ItemLLM{Calls: 2, PromptTokens: 10, CompletionTokens: 5, LatencyMs: 1500, Models: []string{"qwen"}},
		Filter:   &history.ItemFilter{LLMHits: 1, LLMUsed: true, LatencyMs: 300},
		Turns:    []history.ItemTurn{{Speaker: "A", Text: "hi"}},
		LLMCalls: []history.ItemLLMCall{{Kind: "chat", Model: "qwen", Endpoint: "/v1/chat", Request: json.RawMessage(`{"a":1}`), Response: "ok", Error: "boom"}},
	}, {
		ID: "m2", EnqueuedAt: now.UnixMilli(), RequestedBy: "v2", Type: "TTS", Status: "Wait", Request: "queued one",
	}}

	feed := &historyFeed{
		Head:    &historyHead{URL: "/admin/history/feed?after=9", Trigger: historyPanelTrigger},
		Cards:   historyCards(items, true, now),
		NextURL: "/admin/history/feed?before=5",
	}
	out := getString("history_feed.html", feed)
	for _, want := range []string{
		`id="hist_m1"`, `id="history_head"`, `bg-red-500/30">&lt;world&gt;</span>`, `>played<`, `>queued<`, `chan</span>`,
		`hx-trigger="revealed"`, `before=5`, `LLM calls (1)`, `&#34;a&#34;: 1`, `3.1s, played 1s, cut: skip`,
		`LLM 2 calls, 10→5 tok, 1.5s (qwen)`, `filter 1 hit, 0.3s`, `2m ago`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("feed missing %q\n%s", want, out)
		}
	}
	if strings.Contains(out, "{{") || strings.Contains(out, "template:") {
		t.Fatalf("template error in output:\n%s", out)
	}

	viewer := getString("history_feed.html", &historyFeed{Cards: historyCards(items, false, now)})
	if strings.Contains(viewer, "LLM calls") || strings.Contains(viewer, "chan</span>") {
		t.Fatalf("non-admin feed leaks admin fields:\n%s", viewer)
	}
	if !strings.Contains(viewer, "end of history") {
		t.Fatalf("last page has no end marker:\n%s", viewer)
	}
	if empty := getString("history_feed.html", &historyFeed{}); !strings.Contains(empty, "nothing here yet") {
		t.Fatalf("empty feed: %s", empty)
	}
	if e := getString("history_feed.html", &historyFeed{Error: "history unavailable"}); !strings.Contains(e, "failed to load: history unavailable") {
		t.Fatalf("error feed: %s", e)
	}

	cards := historyCards(items, true, now)
	if !cards[0].Final || cards[1].Final {
		t.Fatalf("Processed card must be final and Wait card in flight: %v %v", cards[0].Final, cards[1].Final)
	}
	cards[1].OOB = true
	head := getString("history_feed.html", &historyFeed{
		HeadOnly: true,
		Head:     &historyHead{URL: "/admin/history/feed?after=7&pending=3", Trigger: historyAdminTrigger},
		Cards:    cards[:1],
		Updated:  cards[1:],
	})
	for _, want := range []string{
		`id="history_head" hx-get="/admin/history/feed?after=7&amp;pending=3" hx-trigger="every 3s" hx-swap="outerHTML"`,
		`id="hist_m1" class="rounded border`, `id="hist_m2"`, `hx-swap-oob="outerHTML"`,
	} {
		if !strings.Contains(head, want) {
			t.Errorf("head refresh missing %q\n%s", want, head)
		}
	}
	if strings.Count(head, `hx-swap-oob`) != 1 || strings.Contains(head, "end of history") || strings.Contains(head, "nothing here") {
		t.Fatalf("head refresh must oob-swap only the updated card and carry no sentinel:\n%s", head)
	}

	widget := getString("history.html", &historyWidget{FeedURL: "/admin/history/feed", Channels: []string{"a", "b"}})
	for _, want := range []string{`hx-trigger="load"`, `<option value="a">a</option>`, `name="q"`, `hx-include="#history_filters"`} {
		if !strings.Contains(widget, want) {
			t.Errorf("widget missing %q\n%s", want, widget)
		}
	}
	panel := getString("history.html", &historyWidget{FeedURL: "/control/1/history/feed", Enabled: true})
	if strings.Contains(panel, "<select") || strings.Contains(panel, "Archive unavailable") {
		t.Fatalf("panel widget: %s", panel)
	}
}
