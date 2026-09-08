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

// TestReplayArchive re-judges recent archived requests with the current prompt
// and prints the messages whose masked words changed; ARCHIVE_REPLAY=<count>
// enables it, nothing fails. Built-in policy only: a change that came from a
// streamer rule or from repeat collapse is a replay artifact, not a prompt
// difference.
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

	// recorded AI-request spans are over the "<login> asked me: " lead-in plus
	// the message, so that string is rebuilt below
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
