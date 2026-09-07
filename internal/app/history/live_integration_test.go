//go:build integration

package history

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"app/db"
	"app/pkg/clickhouse"

	"gopkg.in/yaml.v3"
)

// TestLiveExportAndRead runs one exporter tick against the live postgres and
// ClickHouse from cfg/cfg.yaml, then reads the newest page back. It advances
// the export watermark exactly as the running service would.
func TestLiveExportAndRead(t *testing.T) {
	raw, err := os.ReadFile("../../../cfg/cfg.yaml")
	if err != nil {
		t.Skip("no cfg.yaml")
	}
	// only the two blocks needed: importing app/cfg would pull in the api
	// package, which imports this one
	var c struct {
		DB         db.Config         `yaml:"db"`
		ClickHouse clickhouse.Config `yaml:"clickhouse"`
	}
	if err := yaml.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	if c.ClickHouse.Addr == "" {
		t.Skip("clickhouse not configured")
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	pg, err := db.New(ctx, &c.DB)
	if err != nil {
		t.Fatal(err)
	}
	defer pg.Close()
	ch, err := clickhouse.Open(ctx, &c.ClickHouse)
	if err != nil {
		t.Fatal(err)
	}
	defer ch.Close()

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	exp := NewExporter(logger, pg, ch)
	total := 0
	for {
		n, err := exp.Tick(ctx)
		if err != nil {
			t.Fatalf("tick: %v", err)
		}
		total += n
		if n < exp.Batch {
			break
		}
	}
	wm, _ := pg.GetExportWatermark(ctx)
	t.Logf("scanned %d queue rows, watermark now %d", total, wm)

	var counts []struct {
		Table string `ch:"table"`
		Rows  uint64 `ch:"rows"`
	}
	if err := ch.Select(ctx, &counts, `select table, sum(rows) as rows from system.parts where active and database = currentDatabase() group by table order by table`); err != nil {
		t.Fatal(err)
	}
	for _, c := range counts {
		t.Logf("%s: %d rows", c.Table, c.Rows)
	}

	r := NewReader(pg, ch)
	rows, err := r.List(ctx, Query{Limit: 5})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, row := range rows {
		it := ToItem(row, ItemOptions{WithLLMCalls: true})
		t.Logf("%s %-12s %-10s %-9s tracks=%d llm=%d %q", row.EnqueuedAt.Format(time.RFC3339), it.Channel, it.Type, it.Outcome, len(it.Tracks), len(it.LLMCalls), clip(it.Request, 60))
	}
	channels, err := r.Channels(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("channels: %v", channels)
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
