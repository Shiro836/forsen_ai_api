package db

import (
	"context"
	"errors"
	"fmt"
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
	ID int

	UserID int

	TwitchMessage TwitchMessage
}

func (db *DB) PushMsg(ctx context.Context, userID int, msg TwitchMessage) error {
	_, err := db.Exec(ctx, `
		insert into
			msg_queue (user_id, msg, status)
		values
			($1, $2, $3)
	`, userID, msg, StatusWait)
	if err != nil {
		return fmt.Errorf("failed to push message: %w", err)
	}

	return nil
}

var ErrMsgNotFound = errors.New("msg not found")

func (db *DB) GetNextMsg(ctx context.Context, userID int) (*Message, error) {
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
	`).Scan(&msg.ID, &msg.UserID, &msg.TwitchMessage)
	if err != nil {
		if err == ErrNoRows {
			return nil, ErrMsgNotFound
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
