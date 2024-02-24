package conns

import (
	"app/db"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

type Manager struct {
	processor Processor

	ctx context.Context

	wg sync.WaitGroup

	rwMutex sync.RWMutex

	subCount       map[string]int
	dataStreams    map[string][]chan *DataEvent
	isClosed       map[string][]bool
	updateEventsCh map[string]chan *Update

	logger *slog.Logger
}

func NewConnectionManager(ctx context.Context, logger *slog.Logger, processor Processor) *Manager {
	return &Manager{
		ctx: ctx,

		processor: processor,

		subCount:       make(map[string]int, 100),
		dataStreams:    make(map[string][]chan *DataEvent, 100),
		isClosed:       make(map[string][]bool, 100),
		updateEventsCh: make(map[string]chan *Update, 100),

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
	EventTypeText
	EventTypeImage
	EventTypeSetModel
	EventTypeSetMotion
	EventTypeInfo
	EventTypeError
	EventTypePing
	EventTypeSkip
)

type EventType int

func (et EventType) String() string {
	switch et {
	case EventTypeAudio:
		return "event_type_audio"
	case EventTypeText:
		return "event_type_text"
	case EventTypeImage:
		return "event_type_image"
	case EventTypeSetModel:
		return "event_type_model"
	case EventTypeSetMotion:
		return "event_type_motion"
	case EventTypeInfo:
		return "event_type_info"
	case EventTypeError:
		return "event_type_error"
	case EventTypeSkip:
		return "event_type_skip"
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

func (m *Manager) Subscribe(user string) (<-chan *DataEvent, func()) {
	user = strings.ToLower(user)

	m.rwMutex.Lock()
	defer m.rwMutex.Unlock()

	m.subCount[user]++

	m.dataStreams[user] = append(m.dataStreams[user], make(chan *DataEvent))
	m.isClosed[user] = append(m.isClosed[user], false)

	ind := len(m.dataStreams[user]) - 1

	return m.dataStreams[user][ind], func() {
		m.rwMutex.Lock()
		defer m.rwMutex.Unlock()

		m.subCount[user]--

		close(m.dataStreams[user][ind])
		m.isClosed[user][ind] = true
		if m.subCount[user] == 0 {
			delete(m.dataStreams, user)
			delete(m.isClosed, user)
		}
	}
}

func (m *Manager) TryWrite(user string, event *DataEvent) bool {
	wrote := false

loop:
	for i := 0; i < len(m.dataStreams[user]); i++ {
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

				if m.isClosed[user][i] {
					cont_loop = true
					return
				}

				select {
				case m.dataStreams[user][i] <- event:
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

func (m *Manager) NotifyUpdateWhitelist(user *db.Human) {
	m.HandleUser(user)
}

func (m *Manager) NotifyUpdateSettings(login string) {
	m.rwMutex.RLock()
	defer m.rwMutex.RUnlock()

	if ch, ok := m.updateEventsCh[login]; ok {
		select {
		case ch <- &Update{
			UpdateType: RestartProcessor,
		}:
		default:
		}
	}
}

func (m *Manager) SkipMessage(login string, msgID string) {
	m.rwMutex.RLock()
	defer m.rwMutex.RUnlock()

	if ch, ok := m.updateEventsCh[login]; ok {
		select {
		case ch <- &Update{
			UpdateType: SkipMessage,
			Data:       msgID,
		}:
		default:
		}
	}
}

func (m *Manager) HandleUser(user *db.Human) {
	user.Login = strings.ToLower(user.Login)

	logger := m.logger.With("user", user.Login)

	logger.Debug("trying to unlock mutex for HandleUser")

	m.rwMutex.Lock()
	defer m.rwMutex.Unlock()

	if user.BannedBy != nil {
		logger.Debug("stopping processor")

		if ch, ok := m.updateEventsCh[user.Login]; ok {
			close(ch)
			delete(m.updateEventsCh, user.Login)
		}
	} else if _, ok := m.updateEventsCh[user.Login]; !ok {
		m.updateEventsCh[user.Login] = make(chan *Update)

		updates := m.updateEventsCh[user.Login]

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
					return m.TryWrite(user.Login, event)
				}, user.Login); err != nil {
					if errors.Is(err, ErrProcessingEnd) {
						break loop
					} else if errors.Is(err, ErrNoUser) {
						time.Sleep(2 * time.Second)
					} else {
						logger.Error("processor Process error", "err", err)
						time.Sleep(10 * time.Second)
					}
				}

				time.Sleep(time.Second)
			}
		}()
	} else {
		logger.Info("starting processor failed, updates already exists")
	}
}

func (m *Manager) Wait() {
	m.wg.Wait()
}
