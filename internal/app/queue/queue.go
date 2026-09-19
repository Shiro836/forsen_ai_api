// Package queue is the scheduling side of msg_queue: which waiting row plays
// next, which rows yield to it, and how a claimed row is closed out. It knows
// nothing about handlers, rewards or Twitch; producers insert rows through db.
package queue

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"app/db"

	"github.com/google/uuid"
)

var ErrEmpty = errors.New("queue empty")

const watchInterval = time.Second

// Order is groups of classes, highest first; a group's classes rank equally.
type Order [][]db.MsgClass

func (o Order) classes() []db.MsgClass {
	return slices.Concat(o...)
}

type Claimed struct {
	*db.Message
	Class db.MsgClass
}

type Queue struct {
	db *db.DB
}

func New(database *db.DB) *Queue {
	return &Queue{db: database}
}

func (q *Queue) Claim(ctx context.Context, userID uuid.UUID, order Order) (claimed *Claimed, purged int, err error) {
	msg, class, err := q.db.ClaimNextMsg(ctx, userID, order)
	if err != nil {
		if errors.Is(err, db.ErrNoRows) {
			return nil, 0, ErrEmpty
		}
		return nil, 0, fmt.Errorf("claim: %w", err)
	}
	claimed = &Claimed{Message: msg, Class: class}

	if slices.Contains(order.classes(), class) {
		purged, err = q.db.PurgeWaitingClass(ctx, userID, db.MsgClassChat)
		if err != nil {
			return claimed, 0, fmt.Errorf("purge chat: %w", err)
		}
	}

	return claimed, purged, nil
}

func (q *Queue) Purge(ctx context.Context, userID uuid.UUID, class db.MsgClass) (int, error) {
	return q.db.PurgeWaitingClass(ctx, userID, class)
}

func (q *Queue) Complete(ctx context.Context, msgID uuid.UUID) error {
	return q.db.CompleteMsg(ctx, msgID)
}

func (q *Queue) Skip(ctx context.Context, msgID uuid.UUID) error {
	return q.db.UpdateMessageStatus(ctx, msgID, db.MsgStatusDeleted)
}

func (q *Queue) Recover(ctx context.Context, userID uuid.UUID) (int, error) {
	return q.db.RecoverCurrentMessages(ctx, userID)
}

// Producers run in another process, so this polls.
func (q *Queue) WatchPreempt(ctx context.Context, claimed *Claimed, order Order) <-chan struct{} {
	fired := make(chan struct{}, 1)
	if claimed.Class != db.MsgClassChat {
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
				ranked, err := q.db.HasWaitingOfClasses(ctx, claimed.UserID, order.classes())
				if err != nil || !ranked {
					continue
				}
				fired <- struct{}{}
				return
			}
		}
	}()

	return fired
}
