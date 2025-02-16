package pubsub

import (
	"sync"

	"github.com/google/uuid"
)

type topic struct {
	subscribers map[string]func(msg any)

	lock sync.Mutex
}

func newTopic() *topic {
	return &topic{
		subscribers: make(map[string]func(msg any)),
	}
}

func (t *topic) publish(msg any) {
	t.lock.Lock()
	defer t.lock.Unlock()

	for _, fn := range t.subscribers {
		fn(msg)
	}
}

func (t *topic) subscribe(fn func(msg any)) (unsub func()) {
	t.lock.Lock()
	defer t.lock.Unlock()

	id := uuid.NewString()
	t.subscribers[id] = fn

	return func() {
		t.lock.Lock()
		defer t.lock.Unlock()

		delete(t.subscribers, id)
	}
}
