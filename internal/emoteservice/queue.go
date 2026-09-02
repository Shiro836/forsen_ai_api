package emoteservice

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// ErrQueueEmpty is returned by ClaimNext when nothing is pending.
var ErrQueueEmpty = errors.New("emoteservice: classification queue empty")

// Enqueue adds emote ids to the classification queue. Ids still in flight keep
// their attempt count but are raised to the new priority; a row that had already
// spent its attempts gets a fresh budget, since re-queueing a failure is a
// deliberate request to retry it.
//
// force marks the rows for re-judgement rather than catch-up: without it the
// worker reuses a stored verdict whose model and version still match, and a
// caller asking for a second opinion gets a drained queue and nothing else. A
// force already pending is never downgraded by a later plain enqueue.
func (s *Store) Enqueue(ctx context.Context, provider string, emoteIDs []string, priority int, force bool) (int, error) {
	if len(emoteIDs) == 0 {
		return 0, nil
	}
	tag, err := s.pool.Exec(ctx,
		`INSERT INTO emote_queue (provider, emote_id, priority, force)
		 SELECT $3, unnest($1::text[]), $2, $4
		 ON CONFLICT (provider, emote_id) DO UPDATE SET
			status = 'pending',
			attempts = CASE WHEN emote_queue.status = 'failed' THEN 0 ELSE emote_queue.attempts END,
			last_error = CASE WHEN emote_queue.status = 'failed' THEN '' ELSE emote_queue.last_error END,
			priority = GREATEST(emote_queue.priority, EXCLUDED.priority),
			force = emote_queue.force OR EXCLUDED.force,
			updated_at = now()`,
		emoteIDs, priority, NormalizeProvider(provider), force)
	if err != nil {
		return 0, fmt.Errorf("enqueue emotes: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// ClaimNext takes the highest-priority pending emote and marks it processing.
// SKIP LOCKED keeps a second worker from picking the same row.
func (s *Store) ClaimNext(ctx context.Context) (provider, emoteID string, force bool, err error) {
	err = s.pool.QueryRow(ctx,
		`UPDATE emote_queue SET status = 'processing', attempts = attempts + 1, updated_at = now()
		 WHERE (provider, emote_id) = (
			SELECT provider, emote_id FROM emote_queue
			 WHERE status = 'pending'
			 ORDER BY priority DESC, enqueued_at
			 LIMIT 1
			 FOR UPDATE SKIP LOCKED
		 )
		 RETURNING provider, emote_id, force`).Scan(&provider, &emoteID, &force)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", false, ErrQueueEmpty
	}
	if err != nil {
		return "", "", false, fmt.Errorf("claim next emote: %w", err)
	}
	return provider, emoteID, force, nil
}

// MarkDone clears force with the row: the re-judgement it asked for has been
// served, and the next ordinary enqueue must not inherit it. A failure leaves it
// set, so the retry still forces.
func (s *Store) MarkDone(ctx context.Context, provider, emoteID string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE emote_queue SET status = 'done', last_error = '', force = false, updated_at = now()
		 WHERE provider = $1 AND emote_id = $2`,
		NormalizeProvider(provider), emoteID)
	if err != nil {
		return fmt.Errorf("mark done %s: %w", emoteID, err)
	}
	return nil
}

// MarkFailed records the error and requeues the emote until maxAttempts is
// spent, after which it stays failed for a human to look at.
func (s *Store) MarkFailed(ctx context.Context, provider, emoteID, reason string, maxAttempts int) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE emote_queue SET
			status = CASE WHEN attempts >= $4 THEN 'failed' ELSE 'pending' END,
			last_error = $3,
			updated_at = now()
		 WHERE provider = $1 AND emote_id = $2`,
		NormalizeProvider(provider), emoteID, reason, maxAttempts)
	if err != nil {
		return fmt.Errorf("mark failed %s: %w", emoteID, err)
	}
	return nil
}

// RequeueProcessing returns rows a killed worker left claimed. It runs once at
// startup, which is why it does not touch attempts.
func (s *Store) RequeueProcessing(ctx context.Context) (int, error) {
	tag, err := s.pool.Exec(ctx,
		`UPDATE emote_queue SET status = 'pending', updated_at = now() WHERE status = 'processing'`)
	if err != nil {
		return 0, fmt.Errorf("requeue processing: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// EnqueueClassified re-queues rows that were already judged, always forcing:
// asking for a reclassification is asking for the vision call, and without the
// flag the worker would reuse every verdict whose version still matches and
// report a clean drain having judged nothing. By default it
// takes the stale ones; all re-runs everything; classes takes every row
// carrying one of those classes whatever its version, which is what a narrowed
// class definition needs — those rows are current, so staleness would select
// none of them, and re-running the whole registry to fix one class is hours of
// shared GPU for nothing.
func (s *Store) EnqueueClassified(ctx context.Context, version, priority int, all bool, classes []string) (int, error) {
	var filter []string
	if len(classes) > 0 {
		filter = classes
	}
	tag, err := s.pool.Exec(ctx,
		`INSERT INTO emote_queue (provider, emote_id, priority, force)
		 SELECT provider, emote_id, $1, true FROM emote_moderation
		  WHERE classified_at IS NOT NULL
		    AND ($3 OR $4::text[] IS NOT NULL OR classifier_version < $2)
		    AND ($4::text[] IS NULL OR classes && $4::text[])
		 ON CONFLICT (provider, emote_id) DO UPDATE SET
			status = 'pending',
			attempts = CASE WHEN emote_queue.status = 'failed' THEN 0 ELSE emote_queue.attempts END,
			last_error = CASE WHEN emote_queue.status = 'failed' THEN '' ELSE emote_queue.last_error END,
			priority = GREATEST(emote_queue.priority, EXCLUDED.priority),
			force = true,
			updated_at = now()`,
		priority, version, all, filter)
	if err != nil {
		return 0, fmt.Errorf("enqueue classified emotes: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// ClassifierVersionStats counts classified rows per taxonomy generation, which
// is what makes a stale backlog visible.
func (s *Store) ClassifierVersionStats(ctx context.Context) (map[int]int, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT classifier_version, count(*) FROM emote_moderation
		  WHERE classified_at IS NOT NULL GROUP BY classifier_version`)
	if err != nil {
		return nil, fmt.Errorf("classifier version stats: %w", err)
	}
	defer rows.Close()

	out := map[int]int{}
	for rows.Next() {
		var version, n int
		if err := rows.Scan(&version, &n); err != nil {
			return nil, fmt.Errorf("scan classifier version stats: %w", err)
		}
		out[version] = n
	}
	return out, rows.Err()
}

func (s *Store) QueueStats(ctx context.Context) (map[string]int, error) {
	rows, err := s.pool.Query(ctx, `SELECT status, count(*) FROM emote_queue GROUP BY status`)
	if err != nil {
		return nil, fmt.Errorf("queue stats: %w", err)
	}
	defer rows.Close()

	out := map[string]int{}
	for rows.Next() {
		var status string
		var n int
		if err := rows.Scan(&status, &n); err != nil {
			return nil, fmt.Errorf("scan queue stats: %w", err)
		}
		out[status] = n
	}
	return out, rows.Err()
}
