package history

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"app/db"
	"app/internal/app/monitoring"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/google/uuid"
)

// Exporter copies every changed msg_queue row past the watermark into
// ClickHouse. Postgres is the durable buffer: the watermark only moves after
// the inserts succeeded, and the purge never deletes past it, so an archive
// outage stalls the purge instead of losing rows.
type Exporter struct {
	logger *slog.Logger
	db     *db.DB
	ch     driver.Conn

	Interval time.Duration
	Batch    int
}

func NewExporter(logger *slog.Logger, database *db.DB, ch driver.Conn) *Exporter {
	return &Exporter{logger: logger, db: database, ch: ch, Interval: 30 * time.Second, Batch: 500}
}

func (e *Exporter) Run(ctx context.Context) {
	ticker := time.NewTicker(e.Interval)
	defer ticker.Stop()
	for {
		// a full batch means a backlog: keep draining before sleeping again
		for {
			n, err := e.Tick(ctx)
			if err != nil {
				if ctx.Err() == nil {
					e.logger.Error("archive export failed", "err", err)
					monitoring.AppMetrics.ArchiveExportErrors.Inc()
				}
				break
			}
			if n < e.Batch {
				break
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Tick exports one batch and returns how many queue rows it scanned.
func (e *Exporter) Tick(ctx context.Context) (int, error) {
	wm, err := e.db.GetExportWatermark(ctx)
	if err != nil {
		return 0, err
	}

	rows, err := e.db.GetExportBatch(ctx, wm, uuid.Nil, e.Batch)
	if err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		e.observeBacklog(ctx)
		return 0, nil
	}

	var (
		messages []MessageRow
		calls    []LLMCallRow
		tracks   []TTSTrackRow
		filters  []FilterRunRow
	)
	for _, r := range rows {
		if !r.Owned {
			continue
		}
		f := Flatten(r)
		messages = append(messages, f.Message)
		calls = append(calls, f.LLM...)
		tracks = append(tracks, f.Tracks...)
		filters = append(filters, f.Filters...)
	}

	// children first: a failure between batches re-runs the whole tick, and
	// ReplacingMergeTree collapses the duplicates
	if err := insert(ctx, e.ch, "llm_calls", calls); err != nil {
		return 0, err
	}
	if err := insert(ctx, e.ch, "tts_tracks", tracks); err != nil {
		return 0, err
	}
	if err := insert(ctx, e.ch, "filter_runs", filters); err != nil {
		return 0, err
	}
	if err := insert(ctx, e.ch, "messages", messages); err != nil {
		return 0, err
	}

	newWM := rows[len(rows)-1].Updated
	if err := e.db.SetExportWatermark(ctx, newWM); err != nil {
		return 0, err
	}

	monitoring.AppMetrics.ArchiveExportRows.WithLabelValues("messages").Add(float64(len(messages)))
	monitoring.AppMetrics.ArchiveExportRows.WithLabelValues("llm_calls").Add(float64(len(calls)))
	monitoring.AppMetrics.ArchiveExportRows.WithLabelValues("tts_tracks").Add(float64(len(tracks)))
	monitoring.AppMetrics.ArchiveExportRows.WithLabelValues("filter_runs").Add(float64(len(filters)))
	monitoring.AppMetrics.ArchiveExportRows.WithLabelValues("skipped_foreign").Add(float64(len(rows) - len(messages)))
	monitoring.AppMetrics.ArchiveWatermark.Set(float64(newWM))
	e.observeBacklog(ctx)

	return len(rows), nil
}

func (e *Exporter) observeBacklog(ctx context.Context) {
	n, err := e.db.CountUnexported(ctx)
	if err != nil {
		if ctx.Err() == nil {
			e.logger.Warn("failed to count unexported rows", "err", err)
		}
		return
	}
	monitoring.AppMetrics.ArchiveUnexportedRows.Set(float64(n))
}

func insert[T any](ctx context.Context, ch driver.Conn, table string, rows []T) error {
	if len(rows) == 0 {
		return nil
	}
	batch, err := ch.PrepareBatch(ctx, "INSERT INTO "+table)
	if err != nil {
		return fmt.Errorf("prepare %s batch: %w", table, err)
	}
	for i := range rows {
		if err := batch.AppendStruct(&rows[i]); err != nil {
			batch.Abort()
			return fmt.Errorf("append %s row: %w", table, err)
		}
	}
	if err := batch.Send(); err != nil {
		return fmt.Errorf("insert %s: %w", table, err)
	}
	return nil
}
