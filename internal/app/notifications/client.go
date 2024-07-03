package notifications

// there is no guarantee of delivering events, since it's only purpose is to deliver updates to control panel

// shit code LULE LULE LULE

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"
)

func New() *Client {
	return &Client{
		notifiers: make(map[uuid.UUID]chan struct{}),
		mapLock:   &sync.Mutex{},
	}
}

type Client struct {
	notifiers map[uuid.UUID]chan struct{} // these channels are never closed, bad practice XD

	mapLock *sync.Mutex
}

func (c *Client) Notify(userID uuid.UUID) {
	func() {
		c.mapLock.Lock()
		defer c.mapLock.Unlock()

		if _, ok := c.notifiers[userID]; !ok {
			c.notifiers[userID] = make(chan struct{})
		}
	}()

	limit := time.After(10 * time.Millisecond)

	for i := 0; i < 100; i++ { // up to 100 subscribers!!!
		select {
		case <-limit:
			return
		default:
		}

		select {
		case c.notifiers[userID] <- struct{}{}:
			continue
		default: // that means there is no one listening, so we can return
			return
		}
	}
}

func (c *Client) SubscribeForNotification(ctx context.Context, userID uuid.UUID) chan struct{} {
	events := make(chan struct{})
	go func() {
		defer close(events)

		// first acquire notifier channel
		var notifier chan struct{} = nil
		for {
			notifierAcquired := false
			func() {
				c.mapLock.Lock()
				defer c.mapLock.Unlock()

				if val, ok := c.notifiers[userID]; ok {
					notifier = val
					notifierAcquired = true
				}
			}()

			if notifierAcquired {
				break
			}

			select {
			case <-ctx.Done():
				return
			case <-time.After(200 * time.Millisecond):
			}
		}

		for {
			select {
			case <-ctx.Done():
				return
			case <-notifier:
				select {
				case events <- struct{}{}:
				default:
				}

				select { // let Notify func finish sending shit to other subscribers
				case <-ctx.Done():
					return
				case <-time.After(40 * time.Millisecond):
				}
			}
		}
	}()

	return events
}
