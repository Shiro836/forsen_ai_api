package db

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

type ClankerStatus int

const (
	ClankerStatusDeleted    ClankerStatus = 0
	ClankerStatusWait       ClankerStatus = 1
	ClankerStatusProcessed  ClankerStatus = 2
	ClankerStatusProcessing ClankerStatus = 3
)

type ClankerMessage struct {
	ID            uuid.UUID
	ChannelLogin  string
	ChannelUserID int
	SenderLogin   string
	SenderUserID  int
	Message       string
	Status        ClankerStatus
	UniqueID      string
	CreatedAt     time.Time
}

func (db *DB) PushClankerMsg(ctx context.Context, channelLogin string, channelUserID int, senderLogin string, senderUserID int, message, uniqueID string) (uuid.UUID, error) {
	var id uuid.UUID

	err := db.QueryRow(ctx, `
		INSERT INTO clanker_queue (channel_login, channel_user_id, sender_login, sender_user_id, message, unique_id)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (unique_id) WHERE unique_id IS NOT NULL
		DO UPDATE SET unique_id = EXCLUDED.unique_id
		RETURNING id
	`, channelLogin, channelUserID, senderLogin, senderUserID, message, uniqueID).Scan(&id)
	if err != nil {
		return uuid.Nil, fmt.Errorf("failed to push clanker message: %w", err)
	}

	return id, nil
}

// GetNextClankerMsg atomically claims the next waiting message by setting its status to processing.
func (db *DB) GetNextClankerMsg(ctx context.Context) (*ClankerMessage, error) {
	msg := ClankerMessage{}

	err := db.QueryRow(ctx, `
		UPDATE clanker_queue
		SET status = $2
		WHERE id = (
			SELECT id FROM clanker_queue
			WHERE status = $1
			ORDER BY created_at ASC, id ASC
			LIMIT 1
			FOR UPDATE SKIP LOCKED
		)
		RETURNING id, channel_login, channel_user_id, sender_login, sender_user_id, message, created_at
	`, ClankerStatusWait, ClankerStatusProcessing).Scan(&msg.ID, &msg.ChannelLogin, &msg.ChannelUserID, &msg.SenderLogin, &msg.SenderUserID, &msg.Message, &msg.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("failed to get next clanker message: %w", parseErr(err))
	}

	return &msg, nil
}

func (db *DB) UpdateClankerMsgStatus(ctx context.Context, id uuid.UUID, status ClankerStatus) error {
	_, err := db.Exec(ctx, `UPDATE clanker_queue SET status = $1 WHERE id = $2`, status, id)
	if err != nil {
		return fmt.Errorf("failed to update clanker message status: %w", err)
	}

	return nil
}

func (db *DB) CleanClankerQueue(ctx context.Context) error {
	_, err := db.Exec(ctx, `
		DELETE FROM clanker_queue
		WHERE status IN ($1, $2)
		AND created_at < now() - interval '1 hour'
	`, ClankerStatusDeleted, ClankerStatusProcessed)
	if err != nil {
		return fmt.Errorf("failed to clean clanker queue: %w", err)
	}

	// delete stuck processing messages (crashed/stuck workers)
	_, err = db.Exec(ctx, `
		DELETE FROM clanker_queue
		WHERE status = $1
		AND created_at < now() - interval '5 minutes'
	`, ClankerStatusProcessing)
	if err != nil {
		return fmt.Errorf("failed to clean clanker queue: %w", err)
	}

	return nil
}
