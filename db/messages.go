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

	TwitchMessage TwitchMessage
}

func (db *DB) PushMsg(ctx context.Context, userID uuid.UUID, msg TwitchMessage) error {
	_, err := db.Exec(ctx, `
		INSERT INTO
			msg_queue (
				user_id,
				msg,
				status,
				updated
			)
		VALUES ($1, $2, $3, (select coalesce(max(updated) + 1, 1) from msg_queue))
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
