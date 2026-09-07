package db

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

// ExportRow is one version of a queue row as the archive exporter sees it:
// the message with its data and archive documents, the channel login, and
// the reward binding resolved for this channel. Owned is false for redeems of
// rewards not bound in reward_buttons for the channel — traffic we see but do
// not archive.
type ExportRow struct {
	ID           uuid.UUID
	UserID       uuid.UUID
	ChannelLogin string
	Status       MsgStatus
	Updated      int64
	Msg          TwitchMessage
	Data         []byte
	Archive      []byte

	Owned      bool
	RewardType *int
	CardID     *uuid.UUID
	CardName   string
}

const exportSelect = `
	select
		mq.id, mq.user_id, u.twitch_login, mq.status, mq.updated, mq.msg, mq.data, mq.archive,
		(coalesce(mq.msg->>'reward_id', '') = '' or rb.reward_type is not null) as owned,
		rb.reward_type, rb.card_id, coalesce(cc.name, '')
	from msg_queue mq
	join users u on u.id = mq.user_id
	left join lateral (
		select reward_type, card_id
		from reward_buttons rb
		where rb.twitch_reward_id = mq.msg->>'reward_id' and rb.user_id = mq.user_id
		order by reward_type
		limit 1
	) rb on true
	left join char_cards cc on cc.id = rb.card_id
	where mq.updated > $1
`

// GetExportBatch returns rows changed since watermark in updated order,
// including unowned ones so the caller can advance past them. userID narrows
// to one channel (the control panel's tail); Nil means every channel.
func (db *DB) GetExportBatch(ctx context.Context, watermark int64, userID uuid.UUID, limit int) ([]ExportRow, error) {
	rows, err := db.Query(ctx, exportSelect+`
		and ($2::uuid is null or mq.user_id = $2)
		order by mq.updated
		limit $3
	`, watermark, nullableUUID(userID), limit)
	if err != nil {
		return nil, fmt.Errorf("failed to get export batch: %w", err)
	}
	defer rows.Close()

	out := make([]ExportRow, 0, limit)
	for rows.Next() {
		var r ExportRow
		if err := rows.Scan(&r.ID, &r.UserID, &r.ChannelLogin, &r.Status, &r.Updated, &r.Msg, &r.Data, &r.Archive,
			&r.Owned, &r.RewardType, &r.CardID, &r.CardName); err != nil {
			return nil, fmt.Errorf("failed to scan export row: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to read export batch: %w", err)
	}
	return out, nil
}

func nullableUUID(id uuid.UUID) *uuid.UUID {
	if id == uuid.Nil {
		return nil
	}
	return &id
}

func (db *DB) GetExportWatermark(ctx context.Context) (int64, error) {
	var wm int64
	if err := db.QueryRow(ctx, `select coalesce(max(watermark), 0) from export_state`).Scan(&wm); err != nil {
		return 0, fmt.Errorf("failed to get export watermark: %w", err)
	}
	return wm, nil
}

func (db *DB) SetExportWatermark(ctx context.Context, wm int64) error {
	if _, err := db.Exec(ctx, `update export_state set watermark = $1, updated_at = now()`, wm); err != nil {
		return fmt.Errorf("failed to set export watermark: %w", err)
	}
	return nil
}

// CountUnexported is the purge backlog: rows the exporter has not acknowledged.
func (db *DB) CountUnexported(ctx context.Context) (int, error) {
	var n int
	err := db.QueryRow(ctx, `
		select count(*) from msg_queue
		where updated > (select coalesce(max(watermark), 0) from export_state)
	`).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("failed to count unexported rows: %w", err)
	}
	return n, nil
}
