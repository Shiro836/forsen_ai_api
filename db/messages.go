package db

import "context"

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
	panic("not implemented")
}
