package db

import (
	"context"
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

type Message struct {
	ID int

	UserID int

	UserName       string
	Message        string
	TwitchRewardID string
}

func (db *DB) GetNextMsg(ctx context.Context, userID int) (*Message, error) {
	panic("not implemented")
}

func (db *DB) CleanQueue(ctx context.Context) error {
	_, err := db.db.Exec(ctx, `
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
