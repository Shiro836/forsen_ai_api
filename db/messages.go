package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
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

func (db *DB) PushMsg(ctx context.Context, userID uuid.UUID, msg TwitchMessage) error {
	_, err := db.Exec(ctx, `
		INSERT INTO
			msg_queue (
				user_id,
				msg,
				status
			)
		VALUES ($1, $2, $3)
	`, userID, msg, MsgStatusWait)
	if err != nil {
		return fmt.Errorf("failed to push message: %w", err)
	}

	return nil
}

func (db *DB) GetNextMsg(ctx context.Context, userID uuid.UUID) (*Message, error) {
	msg := Message{}

	err := db.QueryRow(ctx, `
		select
			id,
			user_id,
			msg
		from
			msg_queue
		where
			user_id = $1
		and
			status = $2
		limit 1
	`, userID, MsgStatusWait).Scan(&msg.ID, &msg.UserID, &msg.TwitchMessage)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNoRows
		}

		return nil, fmt.Errorf("failed to get next message: %w", err)
	}

	return &msg, nil
}

func (db *DB) CleanQueue(ctx context.Context) error {
	_, err := db.Exec(ctx, `
		delete from
			msg_queue
		where
			status = $1
	`, MsgStatusDeleted)

	if err != nil {
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

func (db *DB) UpdateCurrentMessages(ctx context.Context, userID uuid.UUID) error {
	_, err := db.Exec(ctx, `
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
		return fmt.Errorf("failed to update current message: %w", err)
	}

	return nil
}

func (db *DB) GetMessageUpdates(ctx context.Context, userID uuid.UUID, updated int) ([]*Message, error) {
	rows, err := db.Query(ctx, `
		select
			id,
			user_id,
			status,
			updated,
			msg
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
		err := rows.Scan(&msg.ID, &msg.UserID, &msg.Status, &msg.Updated, &msg.TwitchMessage)
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
