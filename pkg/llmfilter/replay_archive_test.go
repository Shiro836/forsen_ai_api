//go:build integration

package llmfilter_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"app/pkg/clickhouse"
	"app/pkg/textfilter"
)

// TestReplayArchive re-judges recent real requests from the archive with the
// current prompt and prints every message whose masked words changed against
// what production recorded, so a prompt edit is checked on real chat rather
// than only on the corpus. Enabled by ARCHIVE_REPLAY=<message count>; the
// diff is eyeballed, nothing here fails.
//
// It runs the built-in policy only, without the streamer's rules or the
// processor's art and repeat collapse, so a recorded span that came from a
// streamer rule ("no politics") or a whole-message span on repetition spam
// shows up as a change without meaning the prompt behaves differently.
func TestReplayArchive(t *testing.T) {
	limit, _ := strconv.Atoi(os.Getenv("ARCHIVE_REPLAY"))
	if limit <= 0 {
		t.Skip("ARCHIVE_REPLAY not set")
	}

	ctx := context.Background()
	conn, err := clickhouse.Open(ctx, &testCfg.ClickHouse)
	if err != nil || conn == nil {
		t.Fatalf("archive: %v", err)
	}
	defer conn.Close()

	// The AI handler filters the spoken lead-in together with the message and
	// production recorded spans over that string, so it is rebuilt the same way.
	rows, err := conn.Query(ctx, `
		select m.twitch_login, m.reward_type, m.message, f.llm_spans
		from messages m final
		join filter_runs f final on f.msg_id = m.id and f.target = 'request'
		where f.skipped = 0 and m.message != ''
		order by m.enqueued_at desc
		limit ?`, limit)
	if err != nil {
		t.Fatalf("query: %v", err)
	}

	type msg struct {
		text string
		old  []textfilter.Span
	}
	var msgs []msg
	for rows.Next() {
		var login, text, spans string
		var rewardType int8
		if err := rows.Scan(&login, &rewardType, &text, &spans); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if rewardType == 1 {
			text = login + " asked me: " + text
		}
		var old []textfilter.Span
		if spans != "" {
			_ = json.Unmarshal([]byte(spans), &old)
		}
		msgs = append(msgs, msg{text: text, old: old})
	}

	var changed atomic.Int32
	for i, m := range msgs {
		m := m
		t.Run(strconv.Itoa(i), func(t *testing.T) {
			t.Parallel()
			inflight <- struct{}{}
			defer func() { <-inflight }()

			ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
			defer cancel()

			spans, err := newFilter().Spans(ctx, m.text, "")
			if err != nil {
				t.Logf("REPLAY ERROR %q: %v", clip(m.text), err)
				return
			}
			before, after := masked(m.text, m.old), masked(m.text, spans)
			if before != after {
				changed.Add(1)
				t.Logf("REPLAY CHANGED %q\n  was: %s\n  now: %s", clip(m.text), before, after)
			}
		})
	}
	t.Cleanup(func() { fmt.Printf("REPLAY messages=%d changed=%d\n", len(msgs), changed.Load()) })
}

func masked(text string, spans []textfilter.Span) string {
	if len(spans) == 0 {
		return "-"
	}
	r := []rune(text)
	var parts []string
	for _, s := range spans {
		parts = append(parts, strconv.Quote(string(r[s.Start:s.End])))
	}
	return strings.Join(parts, " ")
}

func clip(s string) string {
	r := []rune(s)
	if len(r) > 160 {
		return string(r[:160]) + "…"
	}
	return s
}
