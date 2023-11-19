package conns

import (
	"app/db"
	"app/slg"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

type Manager struct {
	processor Processor

	ctx context.Context

	wg sync.WaitGroup

	rwMutex sync.RWMutex

	subCount       map[string]int
	dataStreams    map[string]chan *DataEvent
	updateEventsCh map[string]chan struct{}
}

func NewConnectionManager(ctx context.Context, processor Processor) *Manager {
	ctx = slg.WithSlog(ctx, slog.With("source", "connection_manager"))

	return &Manager{
		ctx: ctx,

		processor: processor,

		subCount:       make(map[string]int, 100),
		dataStreams:    make(map[string]chan *DataEvent, 100),
		updateEventsCh: make(map[string]chan struct{}, 100),
	}
}

type Audio struct {
	Audio []byte `json:"audio"`
}

type Text struct {
	Text string `json:"text"`
}

const (
	EventTypeAudio EventType = iota + 1
	EventTypeText
	EventTypeInfo
	EventTypeError
)

type EventType int

func (et EventType) String() string {
	switch et {
	case EventTypeAudio:
		return "event_type_audio"
	case EventTypeText:
		return "event_type_text"
	case EventTypeInfo:
		return "event_type_info"
	case EventTypeError:
		return "event_type_error"
	default:
		return "event_type_unknown"
	}
}

type DataEvent struct {
	EventType EventType
	EventData []byte
}

func (d *DataEvent) String() string {
	return fmt.Sprintf("%s:%s", d.EventType.String(), string(d.EventData))
}

func (m *Manager) Unsubscribe(user string) {
	m.rwMutex.Lock()
	defer m.rwMutex.Unlock()

	m.subCount[user]--

	if m.subCount[user] == 0 {
		close(m.dataStreams[user])
		delete(m.dataStreams, user)
	}
}

func (m *Manager) Subscribe(user string) <-chan *DataEvent {
	m.rwMutex.Lock()
	defer m.rwMutex.Unlock()

	m.subCount[user]++

	if m.subCount[user] == 1 {
		m.dataStreams[user] = make(chan *DataEvent)
	}

	return m.dataStreams[user]
}

func (m *Manager) Write(user string, event *DataEvent) {
	for {
		br := false

		func() {
			m.rwMutex.RLock()
			defer m.rwMutex.RUnlock()

			if m.subCount[user] == 0 {
				br = true
				return
			}

			select {
			case m.dataStreams[user] <- event:
				br = true
			default:
			}
		}()

		if br {
			return
		}

		time.Sleep(100 * time.Millisecond)
	}
}

func (m *Manager) NotifyUpdateWhitelist(user *db.Human) {
	m.HandleUser(user)
}

func (m *Manager) NotifyUpdateSettings(login string) {
	m.rwMutex.Lock()
	defer m.rwMutex.Unlock()

	if ch, ok := m.updateEventsCh[login]; ok {
		ch <- struct{}{}
	}
}

func (m *Manager) HandleUser(user *db.Human) {
	m.rwMutex.Lock()
	defer m.rwMutex.Unlock()

	ctx := slg.WithSlog(m.ctx, slog.With("user", user.Login))

	if user.BannedBy != nil {
		slg.GetSlog(ctx).Info("stopping processor")

		if ch, ok := m.updateEventsCh[user.Login]; ok {
			close(ch)
			delete(m.updateEventsCh, user.Login)
		}
	} else if _, ok := m.updateEventsCh[user.Login]; !ok {
		m.updateEventsCh[user.Login] = make(chan struct{})

		updates := m.updateEventsCh[user.Login]

		slg.GetSlog(ctx).Info("starting processor")

		m.wg.Add(1)
		go func() {
			defer slg.GetSlog(ctx).Info("stopped processor")
			defer m.wg.Done()

		loop:
			for {
				select {
				case <-m.ctx.Done():
					break loop
				default:
				}

				if err := m.processor.Process(ctx, updates, func(event *DataEvent) {
					m.Write(user.Login, event)
				}, user.Login); err != nil {
					if errors.Is(err, ErrProcessingEnd) {
						break loop
					}
					slg.GetSlog(ctx).Error("processor Process error", "err", err)
				}
			}
		}()
	}
}

func (m *Manager) Wait() {
	m.wg.Wait()
}
