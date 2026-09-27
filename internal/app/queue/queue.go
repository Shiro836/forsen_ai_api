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

type Order struct {
	// Groups are classes, highest first; a group's classes rank equally.
	Groups [][]db.MsgClass
	// Yielding classes are dropped from the queue and cut off mid-playback by
	// anything ranked above them. Chat always is.
	Yielding []db.MsgClass
}

// above is what outranks class: for a class the order does not rank, all of it.
func (o Order) above(class db.MsgClass) []db.MsgClass {
	for rank, group := range o.Groups {
		if slices.Contains(group, class) {
			return slices.Concat(o.Groups[:rank]...)
		}
	}
	return slices.Concat(o.Groups...)
}

func (o Order) yielding() []db.MsgClass {
	return append([]db.MsgClass{db.MsgClassChat}, o.Yielding...)
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
	msg, class, err := q.db.ClaimNextMsg(ctx, userID, order.Groups)
	if err != nil {
		if errors.Is(err, db.ErrNoRows) {
			return nil, 0, ErrEmpty
		}
		return nil, 0, fmt.Errorf("claim: %w", err)
	}
	claimed = &Claimed{Message: msg, Class: class}

	for _, yielding := range order.yielding() {
		if !slices.Contains(order.above(yielding), class) {
			continue
		}
		n, err := q.db.PurgeWaitingClass(ctx, userID, yielding)
		if err != nil {
			return claimed, purged, fmt.Errorf("purge %s: %w", yielding, err)
		}
		purged += n
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
	above := order.above(claimed.Class)
	if !slices.Contains(order.yielding(), claimed.Class) || len(above) == 0 {
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
				ranked, err := q.db.HasWaitingOfClasses(ctx, claimed.UserID, above)
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
