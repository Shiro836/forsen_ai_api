// Package queue is the scheduling side of msg_queue: which waiting row plays
// next, which rows yield to it, and how a claimed row is closed out. It knows
// nothing about handlers, rewards or Twitch; producers insert rows through db.
package queue

import (
	"context"
	"errors"
	"fmt"
	"time"

	"app/db"

	"github.com/google/uuid"
)

// ErrEmpty is returned by Claim when the user has no waiting row.
var ErrEmpty = errors.New("queue empty")

const watchInterval = time.Second

type Queue struct {
	db *db.DB
}

func New(database *db.DB) *Queue {
	return &Queue{db: database}
}

// yields reports whether rows of the class are dropped (queued) or cut
// (playing) as soon as anything outranking them arrives.
func yields(class db.MsgClass) bool {
	return class == db.MsgClassChat
}

// Claim marks the best waiting row current and returns it, along with how
// many yielding rows were purged because the claimed row outranks them.
func (q *Queue) Claim(ctx context.Context, userID uuid.UUID) (msg *db.Message, purged int, err error) {
	msg, err = q.db.ClaimNextMsg(ctx, userID)
	if err != nil {
		if errors.Is(err, db.ErrNoRows) {
			return nil, 0, ErrEmpty
		}
		return nil, 0, fmt.Errorf("claim: %w", err)
	}

	if msg.Rank > 0 {
		purged, err = q.db.PurgeWaitingClass(ctx, userID, db.MsgClassChat)
		if err != nil {
			return msg, 0, fmt.Errorf("purge yielding rows: %w", err)
		}
	}

	return msg, purged, nil
}

// Purge deletes every waiting row of the class.
func (q *Queue) Purge(ctx context.Context, userID uuid.UUID, class db.MsgClass) (int, error) {
	return q.db.PurgeWaitingClass(ctx, userID, class)
}

// Complete closes a claimed row; a row the skip path already deleted stays deleted.
func (q *Queue) Complete(ctx context.Context, msgID uuid.UUID) error {
	return q.db.CompleteMsg(ctx, msgID)
}

// Skip deletes a row whether it waits or plays.
func (q *Queue) Skip(ctx context.Context, msgID uuid.UUID) error {
	return q.db.UpdateMessageStatus(ctx, msgID, db.MsgStatusDeleted)
}

// Recover closes rows a previous processor run left current.
func (q *Queue) Recover(ctx context.Context, userID uuid.UUID) (int, error) {
	return q.db.RecoverCurrentMessages(ctx, userID)
}

// WatchPreempt delivers one value when a row outranking msg arrives while msg
// plays; for classes that do not yield it never delivers. Producers run in
// another process, so this polls the queue until ctx ends.
func (q *Queue) WatchPreempt(ctx context.Context, msg *db.Message) <-chan struct{} {
	fired := make(chan struct{}, 1)
	if !yields(msg.Class) {
		return fired
	}

	go func() {
		ticker := time.NewTicker(watchInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				outranked, err := q.db.HasWaitingOutranking(ctx, msg.UserID, msg.Rank)
				if err != nil || !outranked {
					continue
				}
				fired <- struct{}{}
				return
			}
		}
	}()

	return fired
}
