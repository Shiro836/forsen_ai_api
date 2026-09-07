package api

import (
	"bytes"
	"encoding/json"
	"html/template"
	"math"
	"strconv"
	"strings"
	"time"

	"app/internal/app/history"
	"app/pkg/textfilter"
)

const (
	historyPageSize  = 50
	historyHeadLimit = 200

	// The control panel's websocket handler raises history-refresh on every
	// queue update; the admin view spans all channels and has no such
	// signal, so it polls.
	historyPanelTrigger = "history-refresh from:body"
	historyAdminTrigger = "every 3s"
)

// historyWidget is the filter bar plus the feed container, which loads its
// first page from FeedURL on its own. Channels is admin-only; empty hides
// the select.
type historyWidget struct {
	FeedURL  string
	Channels []string
	Enabled  bool
}

// historyFeed is one page of cards followed by the sentinel that fetches
// NextURL when scrolled into view. NextURL is empty on the last page.
//
// The newest page also carries Head, an empty element above the cards that
// re-fetches on Trigger and replaces itself with the cards enqueued since
// (Cards) plus in-flight cards re-rendered out of band by id (Updated);
// HeadOnly marks such a refresh response, which has no sentinel.
type historyFeed struct {
	Head     *historyHead
	HeadOnly bool
	Cards    []historyCard
	Updated  []historyCard
	NextURL  string
	Error    string
}

type historyHead struct {
	URL     string
	Trigger string
}

type historyBadge struct {
	Key   string
	Label string
}

type historyCard struct {
	ID          string
	EnqueuedAt  int64
	Final       bool
	OOB         bool
	When        string
	WhenTitle   string
	Channel     string
	RequestedBy string
	Type        string
	CharName    string
	Outcome     historyBadge
	Request     template.HTML
	Response    template.HTML
	ImageURLs   []string
	Turns       []history.ItemTurn
	Error       string
	Tracks      []historyTrack
	Stats       string
	LLMCalls    []historyLLMCall
}

type historyTrack struct {
	Engine   string
	AudioURL string
	Meta     string
}

type historyLLMCall struct {
	Kind     string
	Model    string
	Meta     string
	Request  string
	Response string
	Error    string
}

func historyCards(items []history.Item, admin bool, now time.Time) []historyCard {
	cards := make([]historyCard, 0, len(items))
	for _, it := range items {
		cards = append(cards, toHistoryCard(it, admin, now))
	}
	return cards
}

func toHistoryCard(it history.Item, admin bool, now time.Time) historyCard {
	at := time.UnixMilli(it.EnqueuedAt)
	c := historyCard{
		ID:          it.ID,
		EnqueuedAt:  it.EnqueuedAt,
		Final:       it.Status != "Wait" && it.Status != "Current",
		When:        relTime(now.Sub(at)),
		WhenTitle:   at.UTC().Format("2006-01-02 15:04:05 UTC"),
		RequestedBy: it.RequestedBy,
		Type:        it.Type,
		CharName:    it.CharName,
		Outcome:     outcomeBadge(it),
		Request:     highlightSpans(it.Request, it.RequestSpans),
		Response:    highlightSpans(it.Response, it.ReplySpans),
		ImageURLs:   it.ImageURLs,
		Turns:       it.Turns,
		Error:       it.Error,
		Stats:       statsLine(it),
	}
	if admin {
		c.Channel = it.Channel
	}
	for _, t := range it.Tracks {
		meta := fmtSec(t.AudioSec)
		if t.PlayedSec < t.AudioSec-0.5 {
			meta += ", played " + fmtSec(t.PlayedSec)
		}
		if t.Cut != "" {
			meta += ", cut: " + t.Cut
		}
		if t.FirstChunkMs != 0 {
			meta += ", first audio " + strconv.Itoa(t.FirstChunkMs) + "ms"
		}
		c.Tracks = append(c.Tracks, historyTrack{Engine: t.Engine, AudioURL: t.AudioURL, Meta: meta})
	}
	if admin {
		for _, call := range it.LLMCalls {
			kind := call.Kind
			if kind == "" {
				kind = "call"
			}
			c.LLMCalls = append(c.LLMCalls, historyLLMCall{
				Kind:     kind,
				Model:    call.Model,
				Meta:     call.Endpoint + " · " + strconv.Itoa(call.PromptTokens) + "→" + strconv.Itoa(call.CompletionTokens) + " tok · " + strconv.Itoa(call.LatencyMs) + "ms",
				Request:  prettyJSON(call.Request),
				Response: call.Response,
				Error:    call.Error,
			})
		}
	}
	return c
}

func outcomeBadge(it history.Item) historyBadge {
	switch it.Outcome {
	case "played":
		return historyBadge{"played", "played"}
	case "silent":
		return historyBadge{"silent", "no audio"}
	case "skipped":
		return historyBadge{"skipped", "skipped"}
	case "bulk_skipped":
		return historyBadge{"skipped", "skipped (queue)"}
	case "error", "aborted":
		return historyBadge{"error", it.Outcome}
	case "":
	default:
		return historyBadge{"other", it.Outcome}
	}
	switch it.Status {
	case "Current":
		return historyBadge{"live", "playing"}
	case "Wait":
		return historyBadge{"live", "queued"}
	case "Deleted":
		return historyBadge{"skipped", "deleted"}
	}
	return historyBadge{"other", it.Status}
}

// highlightSpans escapes text and wraps each rune span in a mark.
func highlightSpans(text string, spans []textfilter.Span) template.HTML {
	if len(spans) == 0 {
		return template.HTML(template.HTMLEscapeString(text))
	}
	runes := []rune(text)
	var b strings.Builder
	pos := 0
	for _, s := range spans {
		if s.Start > len(runes) {
			break
		}
		if s.End > len(runes) {
			s.End = len(runes)
		}
		if s.Start > pos {
			b.WriteString(template.HTMLEscapeString(string(runes[pos:s.Start])))
		}
		if s.End > s.Start {
			b.WriteString(`<span class="rounded px-0.5 bg-red-500/30">`)
			b.WriteString(template.HTMLEscapeString(string(runes[s.Start:s.End])))
			b.WriteString(`</span>`)
			pos = s.End
		}
	}
	if pos < len(runes) {
		b.WriteString(template.HTMLEscapeString(string(runes[pos:])))
	}
	return template.HTML(b.String())
}

func statsLine(it history.Item) string {
	var parts []string
	if it.DurationMs != 0 {
		parts = append(parts, "total "+fmtSec(float64(it.DurationMs)/1000))
	}
	if it.LLM != nil {
		s := "LLM " + strconv.Itoa(it.LLM.Calls) + plural(it.LLM.Calls, " call", " calls") +
			", " + strconv.Itoa(it.LLM.PromptTokens) + "→" + strconv.Itoa(it.LLM.CompletionTokens) + " tok" +
			", " + fmtSec(float64(it.LLM.LatencyMs)/1000)
		if len(it.LLM.Models) > 0 {
			s += " (" + strings.Join(it.LLM.Models, ", ") + ")"
		}
		parts = append(parts, s)
	}
	if it.Filter != nil {
		hits := it.Filter.LLMHits + it.Filter.RegexHits
		s := "filter clean"
		if hits > 0 {
			s = "filter " + strconv.Itoa(hits) + plural(hits, " hit", " hits")
		}
		if it.Filter.LLMUsed {
			s += ", " + fmtSec(float64(it.Filter.LatencyMs)/1000)
		} else {
			s += ", regex only"
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, " · ")
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func fmtSec(x float64) string {
	return strconv.FormatFloat(math.Round(x*10)/10, 'f', -1, 64) + "s"
}

func relTime(d time.Duration) string {
	s := int(math.Round(d.Seconds()))
	if s < 60 {
		return strconv.Itoa(s) + "s ago"
	}
	m := int(math.Round(float64(s) / 60))
	if m < 60 {
		return strconv.Itoa(m) + "m ago"
	}
	h := int(math.Round(float64(m) / 60))
	if h < 48 {
		return strconv.Itoa(h) + "h ago"
	}
	return strconv.Itoa(int(math.Round(float64(h)/24))) + "d ago"
}

func prettyJSON(raw json.RawMessage) string {
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		return string(raw)
	}
	return buf.String()
}
