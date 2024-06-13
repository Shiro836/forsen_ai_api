package conns

import (
	"app/db"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
)

type Manager struct {
	processor Processor

	ctx context.Context

	wg sync.WaitGroup

	rwMutex sync.RWMutex

	subCount       map[uuid.UUID]int
	dataStreams    map[uuid.UUID][]chan *DataEvent
	isClosed       map[uuid.UUID][]bool
	updateEventsCh map[uuid.UUID]chan *Update

	logger *slog.Logger
}

func NewConnectionManager(ctx context.Context, logger *slog.Logger, processor Processor) *Manager {
	return &Manager{
		ctx: ctx,

		processor: processor,

		subCount:       make(map[uuid.UUID]int, 100),
		dataStreams:    make(map[uuid.UUID][]chan *DataEvent, 100),
		isClosed:       make(map[uuid.UUID][]bool, 100),
		updateEventsCh: make(map[uuid.UUID]chan *Update, 100),

		logger: logger,
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
	EventTypeVideo
	EventTypeText
	EventTypeImage
	EventTypeSkip
	EventTypePing
)

type EventType int

func (et EventType) String() string {
	switch et {
	case EventTypeAudio:
		return "audio"
	case EventTypeVideo:
		return "video"
	case EventTypeText:
		return "text"
	case EventTypeImage:
		return "image"
	case EventTypeSkip:
		return "skip"
	case EventTypePing:
		return "ping"
	default:
		return "unknown"
	}
}

type DataEvent struct {
	EventType EventType
	EventData []byte
}

func (d *DataEvent) String() string {
	return fmt.Sprintf("%s:%s", d.EventType.String(), string(d.EventData))
}

func (m *Manager) Subscribe(userID uuid.UUID) (<-chan *DataEvent, func()) {
	m.rwMutex.Lock()
	defer m.rwMutex.Unlock()

	m.subCount[userID]++

	m.dataStreams[userID] = append(m.dataStreams[userID], make(chan *DataEvent))
	m.isClosed[userID] = append(m.isClosed[userID], false)

	ind := len(m.dataStreams[userID]) - 1

	return m.dataStreams[userID][ind], func() {
		m.rwMutex.Lock()
		defer m.rwMutex.Unlock()

		m.subCount[userID]--

		close(m.dataStreams[userID][ind])
		m.isClosed[userID][ind] = true
		if m.subCount[userID] == 0 {
			delete(m.dataStreams, userID)
			delete(m.isClosed, userID)
		}
	}
}

func (m *Manager) TryWrite(userID uuid.UUID, event *DataEvent) bool {
	wrote := false

loop:
	for i := 0; i < len(m.dataStreams[userID]); i++ {
		select {
		case <-m.ctx.Done():
			return wrote
		default:
		}

		for j := 0; j < 5; j++ {
			cont_loop := false
			func() {
				m.rwMutex.RLock()
				defer m.rwMutex.RUnlock()

				if len(m.isClosed[userID]) <= i {
					cont_loop = true
					return
				}

				if m.isClosed[userID][i] {
					cont_loop = true
					return
				}

				select {
				case m.dataStreams[userID][i] <- event:
					wrote = true
					cont_loop = true
				case <-m.ctx.Done():
					return
				default:
				}
			}()
			if cont_loop {
				continue loop
			}

			time.Sleep(50 * time.Millisecond)
		}
	}

	return wrote
}

func (m *Manager) NotifyUpdateSettings(userID uuid.UUID) {
	m.rwMutex.RLock()
	defer m.rwMutex.RUnlock()

	if ch, ok := m.updateEventsCh[userID]; ok {
		select {
		case ch <- &Update{
			UpdateType: RestartProcessor,
		}:
		default:
		}
	}
}

func (m *Manager) SkipMessage(userID uuid.UUID, msgID string) {
	m.rwMutex.RLock()
	defer m.rwMutex.RUnlock()

	if ch, ok := m.updateEventsCh[userID]; ok {
		select {
		case ch <- &Update{
			UpdateType: SkipMessage,
			Data:       msgID,
		}:
		default:
		}
	}
}

func (m *Manager) DisableUser(userID uuid.UUID) {
	m.rwMutex.Lock()
	defer m.rwMutex.Unlock()

	if ch, ok := m.updateEventsCh[userID]; ok {
		close(ch)
		delete(m.updateEventsCh, userID)
	}
}

func (m *Manager) HandleUser(user *db.User) {
	logger := m.logger.With("user", user.TwitchLogin)

	logger.Debug("trying to unlock mutex for HandleUser")

	m.rwMutex.Lock()
	defer m.rwMutex.Unlock()

	if _, ok := m.updateEventsCh[user.ID]; !ok {
		m.updateEventsCh[user.ID] = make(chan *Update)

		updates := m.updateEventsCh[user.ID]

		logger.Info("starting processor")

		m.wg.Add(1)
		go func() {
			defer logger.Info("stopped processor")
			defer m.wg.Done()

		loop:
			for {
				select {
				case <-m.ctx.Done():
					break loop
				default:
				}

				if err := m.processor.Process(m.ctx, updates, func(event *DataEvent) bool {
					return m.TryWrite(user.ID, event)
				}, user); err != nil {
					if errors.Is(err, ErrProcessingEnd) {
						break loop
					} else if errors.Is(err, ErrNoUser) {
						select {
						case <-time.After(time.Second):
						case <-m.ctx.Done():
						}
					} else {
						logger.Error("processor Process error", "err", err)
						select {
						case <-time.After(10 * time.Second):
						case <-m.ctx.Done():
						}
					}
				}

				select {
				case <-time.After(time.Second):
				case <-m.ctx.Done():
				}
			}
		}()
	} else {
		logger.Info("starting processor failed, updates already exists")
	}
}

func (m *Manager) Wait() {
	m.wg.Wait()
}
