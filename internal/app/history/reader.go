package history

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"app/db"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/google/uuid"
)

// Query selects a page of history, newest first. ChannelID narrows to one
// channel (the control panel); Nil with an optional ChannelLogin is the admin
// view across channels. Before pages by enqueued time (exclusive) and Since
// bounds it from below (inclusive) for live head refreshes; Search matches
// the requester, the message or the reply, case-insensitively.
type Query struct {
	ChannelID    uuid.UUID
	ChannelLogin string
	Before       time.Time
	Since        time.Time
	Search       string
	Limit        int
}

// Reader serves history pages from ClickHouse merged with the postgres tail
// the exporter has not acknowledged yet, so the last ~30 s are never missing.
// A nil ch serves the tail only.
type Reader struct {
	db *db.DB
	ch driver.Conn
}

func NewReader(database *db.DB, ch driver.Conn) *Reader {
	return &Reader{db: database, ch: ch}
}

func (r *Reader) Enabled() bool { return r.ch != nil }

func (r *Reader) List(ctx context.Context, q Query) ([]MessageRow, error) {
	if q.Limit <= 0 || q.Limit > 200 {
		q.Limit = 50
	}
	if q.Before.IsZero() {
		q.Before = time.Now().Add(time.Hour)
	}

	byID := map[uuid.UUID]MessageRow{}

	if r.ch != nil {
		rows, err := r.queryCH(ctx, q)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			byID[row.ID] = row
		}
	}

	tail, err := r.tail(ctx, q)
	if err != nil {
		return nil, err
	}
	for _, row := range tail {
		if cur, ok := byID[row.ID]; !ok || row.Updated > cur.Updated {
			byID[row.ID] = row
		}
	}

	out := make([]MessageRow, 0, len(byID))
	for _, row := range byID {
		out = append(out, row)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].EnqueuedAt.Equal(out[j].EnqueuedAt) {
			return out[i].EnqueuedAt.After(out[j].EnqueuedAt)
		}
		return out[i].ID.String() > out[j].ID.String()
	})
	if len(out) > q.Limit {
		out = out[:q.Limit]
	}
	return out, nil
}

func (r *Reader) queryCH(ctx context.Context, q Query) ([]MessageRow, error) {
	var (
		where = []string{"enqueued_at < {before:DateTime64(3)}"}
		args  = []any{clickhouse.DateNamed("before", q.Before, clickhouse.MilliSeconds)}
	)
	if !q.Since.IsZero() {
		where = append(where, "enqueued_at >= {since:DateTime64(3)}")
		args = append(args, clickhouse.DateNamed("since", q.Since, clickhouse.MilliSeconds))
	}
	if q.ChannelID != uuid.Nil {
		where = append(where, "channel_id = {channel:UUID}")
		args = append(args, clickhouse.Named("channel", q.ChannelID.String()))
	} else if q.ChannelLogin != "" {
		where = append(where, "channel_login = {login:String}")
		args = append(args, clickhouse.Named("login", q.ChannelLogin))
	}
	if s := strings.TrimSpace(q.Search); s != "" {
		where = append(where, "(positionCaseInsensitiveUTF8(message, {q:String}) > 0 or positionCaseInsensitiveUTF8(ai_response, {q:String}) > 0 or positionCaseInsensitiveUTF8(twitch_login, {q:String}) > 0)")
		args = append(args, clickhouse.Named("q", s))
	}

	var rows []MessageRow
	err := r.ch.Select(ctx, &rows, `
		select * from messages final
		where `+strings.Join(where, " and ")+`
		order by enqueued_at desc, id desc
		limit `+strconv.Itoa(q.Limit)+`
	`, args...)
	if err != nil {
		return nil, fmt.Errorf("history query: %w", err)
	}
	return rows, nil
}

// tail flattens the queue rows the exporter has not passed yet, filtered the
// same way as the ClickHouse page.
func (r *Reader) tail(ctx context.Context, q Query) ([]MessageRow, error) {
	wm, err := r.db.GetExportWatermark(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := r.db.GetExportBatch(ctx, wm, q.ChannelID, 2000)
	if err != nil {
		return nil, err
	}

	search := strings.ToLower(strings.TrimSpace(q.Search))
	out := make([]MessageRow, 0, len(rows))
	for _, row := range rows {
		if !row.Owned {
			continue
		}
		if q.ChannelLogin != "" && row.ChannelLogin != q.ChannelLogin {
			continue
		}
		m := Flatten(row).Message
		if !m.EnqueuedAt.Before(q.Before) || m.EnqueuedAt.Before(q.Since) {
			continue
		}
		if search != "" && !strings.Contains(strings.ToLower(m.Message), search) &&
			!strings.Contains(strings.ToLower(m.AIResponse), search) &&
			!strings.Contains(strings.ToLower(m.TwitchLogin), search) {
			continue
		}
		out = append(out, m)
	}
	return out, nil
}

// Channels lists the channel logins present in the archive, for the admin
// filter.
func (r *Reader) Channels(ctx context.Context) ([]string, error) {
	if r.ch == nil {
		return nil, nil
	}
	var rows []struct {
		Login string `ch:"channel_login"`
	}
	if err := r.ch.Select(ctx, &rows, `select distinct channel_login from messages order by channel_login`); err != nil {
		return nil, fmt.Errorf("history channels: %w", err)
	}
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.Login)
	}
	return out, nil
}
