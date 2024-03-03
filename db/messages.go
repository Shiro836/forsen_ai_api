package db

import (
	"context"
	"fmt"
)

const (
	StateDeleted   = "deleted"
	StateWait      = "wait"
	StateProcessed = "processed"
	StateCurrent   = "current"
)

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
			messages
		where
			state = $1
	`, StateDeleted)

	if err != nil {
		return fmt.Errorf("failed to clean queue: %w", err)
	}

	return nil
}
