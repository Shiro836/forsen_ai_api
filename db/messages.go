package db

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

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
	TwitchLogin string `json:"twitch_login"`
	Message     string `json:"message"`
	RewardID    string `json:"reward_id"`
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
				unique_id
			)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (unique_id) WHERE unique_id IS NOT NULL 
		DO UPDATE SET unique_id = EXCLUDED.unique_id
		RETURNING id
	`, userID, msg, MsgStatusWait, data, uniqueID).Scan(&id)

	if err != nil {
		return uuid.Nil, fmt.Errorf("failed to push ingest message: %w", err)
	}

	return id, nil
}

func (db *DB) GetNextMsg(ctx context.Context, userID uuid.UUID) (*Message, error) {
	msg := Message{}

	err := db.QueryRow(ctx, `
		select
			id,
			user_id,
			msg,
			data
		from
			msg_queue
		where
			user_id = $1
		and
			status = $2
        order by
            updated asc,
            id asc
		limit 1
	`, userID, MsgStatusWait).Scan(&msg.ID, &msg.UserID, &msg.TwitchMessage, &msg.Data)
	if err != nil {
		return nil, fmt.Errorf("failed to get next message: %w", parseErr(err))
	}

	return &msg, nil
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

func (db *DB) CleanQueue(ctx context.Context) error {
	_, err := db.Exec(ctx, `
		delete from
			msg_queue
		where
			(status = $1 or status = $2)
		and
			updated < currval('updated_seq') - 200
	`, MsgStatusDeleted, MsgStatusProcessed)

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

	ShowImages *bool    `json:"show_images,omitempty"`
	ImageIDs   []string `json:"image_ids,omitempty"`
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

func (db *DB) UpdateCurrentMessages(ctx context.Context, userID uuid.UUID) (cntUpdated int, err error) {
	tag, err := db.Exec(ctx, `
		update
			msg_queue
		set
			status = $1,
			updated = nextval('updated_seq')
		where
			user_id = $2
		and
			status = $3
	`, MsgStatusProcessed, userID, MsgStatusCurrent)
	if err != nil {
		return 0, fmt.Errorf("failed to update current message: %w", err)
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
