package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"app/pkg/textfilter"
	"app/pkg/tools"

	sq "github.com/Masterminds/squirrel"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

var psql = sq.StatementBuilder.PlaceholderFormat(sq.Dollar)

var bumpUpdated = sq.Expr("nextval('updated_seq')")

type MsgStatus int

const (
	MsgStatusDeleted MsgStatus = iota
	MsgStatusWait
	MsgStatusProcessed
	MsgStatusCurrent
)

func (s MsgStatus) String() string {
	switch s {
	case MsgStatusDeleted:
		return "Deleted"
	case MsgStatusWait:
		return "Wait"
	case MsgStatusProcessed:
		return "Processed"
	case MsgStatusCurrent:
		return "Current"
	default:
		return ""
	}
}

type TwitchMessage struct {
	TwitchLogin  string `json:"twitch_login"`
	TwitchUserID int    `json:"twitch_user_id,omitempty"`
	Message      string `json:"message"`
	RewardID     string `json:"reward_id"`

	Event *EventMeta `json:"event,omitempty"`
}

type EventKind string

const (
	EventKindPointsRedeem  EventKind = "points_redeem"
	EventKindCustomPowerUp EventKind = "custom_power_up"
)

const BitsPerUSD = 100

// EventMeta is what only Twitch's event feed knows about a message; chat
// delivers the same redemption without any of it.
type EventMeta struct {
	Kind         EventKind `json:"kind"`
	RedemptionID string    `json:"redemption_id,omitempty"`
	Bits         int       `json:"bits,omitempty"`
	USD          float64   `json:"usd,omitempty"`
}

type MsgClass string

const (
	MsgClassChat     MsgClass = "chat"
	MsgClassReward   MsgClass = "reward"
	MsgClassBits     MsgClass = "bits"
	MsgClassDonation MsgClass = "donation"
	MsgClassSub      MsgClass = "sub"
	MsgClassRaid     MsgClass = "raid"
	MsgClassStreak   MsgClass = "streak"
	MsgClassFollow   MsgClass = "follow"
	MsgClassUnrouted MsgClass = "unrouted"
)

const msgClassExpr = `(case
	when coalesce(msg_queue.msg->>'reward_id', '') = '' then 'chat'
	when not exists (select 1 from reward_buttons rb where rb.twitch_reward_id = msg_queue.msg->>'reward_id') then 'unrouted'
	when msg_queue.msg->'event'->>'kind' = 'custom_power_up' then 'bits'
	else 'reward'
end)`

func classNames(classes []MsgClass) []string {
	names := make([]string, len(classes))
	for i, class := range classes {
		names[i] = string(class)
	}
	return names
}

type Message struct {
	ID uuid.UUID

	UserID uuid.UUID

	Status MsgStatus

	TwitchMessage TwitchMessage

	Updated int

	Data []byte
}

func (db *DB) PushMsg(ctx context.Context, userID uuid.UUID, msg TwitchMessage, data *MessageData) (uuid.UUID, error) {
	var id uuid.UUID

	err := db.QueryRow(ctx, `
		INSERT INTO
			msg_queue (
				user_id,
				msg,
				status,
				data
			)
		VALUES ($1, $2, $3, $4)
		RETURNING id
	`, userID, msg, MsgStatusWait, data).Scan(&id)
	if err != nil {
		return uuid.Nil, fmt.Errorf("failed to push message: %w", err)
	}

	return id, nil
}

func (db *DB) PushIngestMsg(ctx context.Context, userID uuid.UUID, msg TwitchMessage, data *MessageData, uniqueID string) (uuid.UUID, error) {
	var id uuid.UUID

	// We use ON CONFLICT DO UPDATE to ensure RETURNING works.
	// We update the updated timestamp to signal it was seen again, or just a dummy update.

	err := db.QueryRow(ctx, `
		INSERT INTO
			msg_queue (
				user_id,
				msg,
				status,
				data,
				unique_id,
				updated
			)
		VALUES ($1, $2, $3, $4, $5, nextval('updated_seq'))
		ON CONFLICT (unique_id) WHERE unique_id IS NOT NULL
		DO UPDATE SET unique_id = EXCLUDED.unique_id
		RETURNING id
	`, userID, msg, MsgStatusWait, data, uniqueID).Scan(&id)

	if err != nil {
		return uuid.Nil, fmt.Errorf("failed to push ingest message: %w", err)
	}

	return id, nil
}

// Groups play in the given order and classes sharing a group rank equally;
// classes in no group play last. A group of several classes plays its bigger
// dollar amounts first, everything else plays in arrival order.
func (db *DB) ClaimNextMsg(ctx context.Context, userID uuid.UUID, order [][]MsgClass) (*Message, MsgClass, error) {
	var (
		classes []string
		ranks   []int
		merged  []bool
	)
	for rank, group := range order {
		for _, class := range group {
			classes = append(classes, string(class))
			ranks = append(ranks, rank)
			merged = append(merged, len(group) > 1)
		}
	}

	pick := sq.Select("msg_queue.id").
		From("msg_queue").
		Where(sq.Eq{"msg_queue.user_id": userID, "msg_queue.status": MsgStatusWait}).
		OrderByClause("(?::int[])[array_position(?::text[], "+msgClassExpr+")] nulls last", ranks, classes).
		OrderByClause("case when (?::bool[])[array_position(?::text[], "+msgClassExpr+")] then (msg_queue.msg->'event'->>'usd')::numeric end desc nulls last", merged, classes).
		// ids are uuid v7, so id order is arrival order
		OrderBy("msg_queue.id").
		Limit(1)

	query, args, err := psql.Update("msg_queue").
		Set("status", MsgStatusCurrent).
		Set("updated", bumpUpdated).
		Where(sq.Expr("msg_queue.id = (?)", pick)).
		Suffix("returning msg_queue.id, msg_queue.user_id, msg_queue.msg, msg_queue.data, " + msgClassExpr).
		ToSql()
	if err != nil {
		return nil, "", fmt.Errorf("failed to build claim query: %w", err)
	}

	var (
		msg   Message
		class MsgClass
	)
	if err := db.QueryRow(ctx, query, args...).Scan(&msg.ID, &msg.UserID, &msg.TwitchMessage, &msg.Data, &class); err != nil {
		return nil, "", fmt.Errorf("failed to claim next message: %w", parseErr(err))
	}

	return &msg, class, nil
}

func (db *DB) GetMessageByID(ctx context.Context, msgID uuid.UUID) (*Message, error) {
	msg := Message{}

	err := db.QueryRow(ctx, `
		select
			id,
			user_id,
			status,
			updated,
			msg,
			data
		from
			msg_queue
		where
			id = $1
	`, msgID).Scan(&msg.ID, &msg.UserID, &msg.Status, &msg.Updated, &msg.TwitchMessage, &msg.Data)
	if err != nil {
		return nil, fmt.Errorf("failed to get message by id: %w", parseErr(err))
	}

	return &msg, nil
}

// CleanQueue purges finished rows beyond the last ~200 updates. With
// respectWatermark it never deletes a row the archive exporter has not
// acknowledged, so an archive outage grows the queue instead of losing rows.
func (db *DB) CleanQueue(ctx context.Context, respectWatermark bool) error {
	_, err := db.Exec(ctx, `
		delete from
			msg_queue
		where
			(status = $1 or status = $2)
		and
			updated < currval('updated_seq') - 200
		and
			(not $3 or updated <= (select coalesce(max(watermark), 0) from export_state))
	`, MsgStatusDeleted, MsgStatusProcessed, respectWatermark)

	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "55000" {
			return nil
		}

		return fmt.Errorf("failed to clean queue: %w", err)
	}

	return nil
}

func (db *DB) UpdateMessageStatus(ctx context.Context, msgID uuid.UUID, status MsgStatus) error {
	_, err := db.Exec(ctx, `
		update
			msg_queue
		set
			status = $1,
			updated = nextval('updated_seq')
		where
			id = $2
	`, status, msgID)
	if err != nil {
		return fmt.Errorf("failed to update message status: %w", err)
	}

	return nil
}

type MessageData struct {
	AIResponse string `json:"ai_response,omitzero"`

	FilteredText      []textfilter.Span `json:"filtered_text,omitempty"`
	RequestFiltered   []textfilter.Span `json:"request_filtered,omitempty"`
	RequesterFiltered []textfilter.Span `json:"requester_filtered,omitempty"`

	ShowImages *bool    `json:"show_images,omitempty"`
	ImageIDs   []string `json:"image_ids,omitempty"`
}

func (db *DB) SetMsgEvent(ctx context.Context, msgID uuid.UUID, event *EventMeta) error {
	encoded, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("failed to encode message event: %w", err)
	}

	query, args, err := psql.Update("msg_queue").
		Set("msg", sq.Expr("jsonb_set(msg, '{event}', ?::jsonb)", string(encoded))).
		Set("updated", bumpUpdated).
		Where(sq.Eq{"id": msgID}).
		ToSql()
	if err != nil {
		return fmt.Errorf("failed to build message event query: %w", err)
	}

	if _, err := db.Exec(ctx, query, args...); err != nil {
		return fmt.Errorf("failed to set message event: %w", err)
	}

	return nil
}

func (db *DB) UpdateMessageData(ctx context.Context, msgID uuid.UUID, data *MessageData) error {
	_, err := db.Exec(ctx, `
		update
			msg_queue
		set
			data = coalesce(data, '{}'::jsonb) || $1::jsonb,
			updated = nextval('updated_seq')
		where
			id = $2
	`, data, msgID)
	if err != nil {
		return fmt.Errorf("failed to update message data: %w", err)
	}

	return nil
}

// UpdateMessageArchive stores a message's telemetry (pkg/archive) in the side
// column. The updated bump is what carries the row past a skip that already
// marked it Deleted, so the exporter never passes it before the archive lands.
func (db *DB) UpdateMessageArchive(ctx context.Context, msgID uuid.UUID, archive any) error {
	_, err := db.Exec(ctx, `
		update
			msg_queue
		set
			archive = $1::jsonb,
			updated = nextval('updated_seq')
		where
			id = $2
	`, archive, msgID)
	if err != nil {
		return fmt.Errorf("failed to update message archive: %w", err)
	}

	return nil
}

func ParseMessageData(data []byte) (*MessageData, error) {
	if len(data) == 0 {
		return &MessageData{}, nil
	}

	msgData := MessageData{}
	err := json.Unmarshal(data, &msgData)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal message data: %w", err)
	}

	return &msgData, nil
}

func (db *DB) CompleteMsg(ctx context.Context, msgID uuid.UUID) error {
	query, args, err := psql.Update("msg_queue").
		Set("status", MsgStatusProcessed).
		Set("updated", bumpUpdated).
		// a skipped row is already Deleted and must stay that way
		Where(sq.Eq{"id": msgID, "status": MsgStatusCurrent}).
		ToSql()
	if err != nil {
		return fmt.Errorf("failed to build complete query: %w", err)
	}

	if _, err := db.Exec(ctx, query, args...); err != nil {
		return fmt.Errorf("failed to complete message: %w", err)
	}

	return nil
}

func (db *DB) RecoverCurrentMessages(ctx context.Context, userID uuid.UUID) (int, error) {
	query, args, err := psql.Update("msg_queue").
		Set("status", MsgStatusProcessed).
		Set("updated", bumpUpdated).
		Where(sq.Eq{"user_id": userID, "status": MsgStatusCurrent}).
		ToSql()
	if err != nil {
		return 0, fmt.Errorf("failed to build recover query: %w", err)
	}

	tag, err := db.Exec(ctx, query, args...)
	if err != nil {
		return 0, fmt.Errorf("failed to recover current messages: %w", err)
	}

	return int(tag.RowsAffected()), nil
}

func (db *DB) HasWaitingOfClasses(ctx context.Context, userID uuid.UUID, classes []MsgClass) (bool, error) {
	waiting := sq.Select("1").
		From("msg_queue").
		Where(sq.Eq{"msg_queue.user_id": userID, "msg_queue.status": MsgStatusWait}).
		Where(sq.Expr(msgClassExpr+" = any(?::text[])", classNames(classes)))

	query, args, err := psql.Select().Column(sq.Expr("exists(?)", waiting)).ToSql()
	if err != nil {
		return false, fmt.Errorf("failed to build waiting classes query: %w", err)
	}

	var exists bool
	if err := db.QueryRow(ctx, query, args...).Scan(&exists); err != nil {
		return false, fmt.Errorf("failed to check for waiting classes: %w", err)
	}

	return exists, nil
}

func (db *DB) PurgeWaitingClass(ctx context.Context, userID uuid.UUID, class MsgClass) (int, error) {
	query, args, err := psql.Update("msg_queue").
		Set("status", MsgStatusDeleted).
		Set("updated", bumpUpdated).
		Where(sq.Eq{"msg_queue.user_id": userID, "msg_queue.status": MsgStatusWait}).
		Where(sq.Expr(msgClassExpr+" = ?", string(class))).
		ToSql()
	if err != nil {
		return 0, fmt.Errorf("failed to build purge query: %w", err)
	}

	tag, err := db.Exec(ctx, query, args...)
	if err != nil {
		return 0, fmt.Errorf("failed to purge waiting %s messages: %w", class, err)
	}

	return int(tag.RowsAffected()), nil
}

func (db *DB) GetMessageUpdates(ctx context.Context, userID uuid.UUID, updated int) ([]*Message, error) {
	rows, err := db.Query(ctx, `
		select
			id,
			user_id,
			status,
			updated,
			msg,
			data
		from
			msg_queue
		where
			user_id = $1
		and
			updated > $2
	`, userID, updated)
	if err != nil {
		return nil, fmt.Errorf("failed to get all messages: %w", err)
	}

	messages := make([]*Message, 0, 20)
	for rows.Next() {
		var msg Message
		err := rows.Scan(&msg.ID, &msg.UserID, &msg.Status, &msg.Updated, &msg.TwitchMessage, &msg.Data)
		if err != nil {
			return nil, fmt.Errorf("failed to scan message: %w", err)
		}

		messages = append(messages, &msg)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to scan message: %w", err)
	}

	return messages, nil
}

// UnboundRewardID is a custom reward id seen in a user's ingested messages that
// no reward_buttons row binds to a character or special reward. SeenAt is the
// arrival time of the newest message carrying that id.
type UnboundRewardID struct {
	RewardID string
	Message  string
	SeenAt   time.Time
}

// GetUnboundRewardIDs returns the user's most recently seen unbound reward ids,
// newest first, one row per distinct id.
func (db *DB) GetUnboundRewardIDs(ctx context.Context, userID uuid.UUID, limit int) ([]*UnboundRewardID, error) {
	rows, err := db.Query(ctx, `
		select
			reward_id,
			message,
			id
		from (
			select distinct on (mq.msg->>'reward_id')
				mq.msg->>'reward_id' as reward_id,
				coalesce(mq.msg->>'message', '') as message,
				mq.id as id
			from
				msg_queue mq
			where
				mq.user_id = $1
			and
				coalesce(mq.msg->>'reward_id', '') != ''
			and not exists (
				select 1
				from reward_buttons rb
				where rb.twitch_reward_id = mq.msg->>'reward_id'
			)
			order by mq.msg->>'reward_id', mq.id desc
		) latest
		order by id desc
		limit $2
	`, userID, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to get unbound reward ids: %w", err)
	}
	defer rows.Close()

	unbound := make([]*UnboundRewardID, 0, limit)
	for rows.Next() {
		var (
			msgID  uuid.UUID
			reward UnboundRewardID
		)

		if err := rows.Scan(&reward.RewardID, &reward.Message, &msgID); err != nil {
			return nil, fmt.Errorf("failed to scan unbound reward id: %w", err)
		}

		reward.SeenAt = tools.UUIDToTime(msgID)

		unbound = append(unbound, &reward)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to get unbound reward ids: %w", err)
	}

	return unbound, nil
}
