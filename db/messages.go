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
	StatusDeleted MsgStatus = iota
	StatusWait
	StatusProcessed
	StatusCurrent
)

func (s MsgStatus) String() string {
	switch s {
	case StatusDeleted:
		return "Deleted"
	case StatusWait:
		return "Wait"
	case StatusProcessed:
		return "Processed"
	case StatusCurrent:
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
	`, userID, msg, StatusWait)
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
	`, userID, StatusWait).Scan(&msg.ID, &msg.UserID, &msg.TwitchMessage)
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
	`, StatusDeleted)

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

func (db *DB) UpdateCurrentMessage(ctx context.Context, userID uuid.UUID) error {
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
	`, StatusProcessed, userID, StatusCurrent)
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
		err := rows.Scan(&msg.ID, &msg.UserID, &msg.Status, &msg.TwitchMessage)
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
