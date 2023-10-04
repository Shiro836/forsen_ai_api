package main

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"app/db"

	"golang.org/x/exp/slog"
)

type SpeechTiming struct {
	Start  time.Duration `json:"start"`
	Length time.Duration `json:"len"`
}

type Speech struct {
	Timings []SpeechTiming `json:"speech_timings"`
	Audio   []byte         `json:"audio"`
}

const (
	EventTypeSpeech = iota + 1
	EventTypeInfo
	EventTypeError
)

type DataEvent struct {
	EventType int
	EventData []byte
}

type ConnectionManager struct {
	ctx context.Context

	wg sync.WaitGroup

	rwMutex sync.RWMutex

	subCount       map[string]int
	dataStreams    map[string]chan *DataEvent
	updateEventsCh map[string]chan struct{}

	slog *slog.Logger
}

func NewConnectionManager(ctx context.Context) *ConnectionManager {
	return &ConnectionManager{
		ctx: ctx,

		updateEventsCh: make(map[string]chan struct{}, 100),

		slog: slog.With("source", "connection_manager"),
	}
}

func (cm *ConnectionManager) Unsubscribe(user string) {
	cm.rwMutex.Lock()
	defer cm.rwMutex.Unlock()

	cm.subCount[user]--

	if cm.subCount[user] == 0 {
		close(cm.dataStreams[user])
		delete(cm.dataStreams, user)
	}
}

func (cm *ConnectionManager) Subscribe(user string) <-chan *DataEvent {
	cm.rwMutex.Lock()
	defer cm.rwMutex.Unlock()

	cm.subCount[user]++

	if cm.subCount[user] == 1 {
		cm.dataStreams[user] = make(chan *DataEvent)
	}

	return cm.dataStreams[user]
}

var (
	ErrNoConsumers = errors.New("no consumers")
	ErrNoProducers = errors.New("no producers")
)

func (cm *ConnectionManager) RecieveChan(user string) (chan *DataEvent, error) {
	if stream, ok := cm.dataStreams[user]; !ok {
		return nil, ErrNoProducers
	} else {
		return stream, nil
	}
}

func (cm *ConnectionManager) Write(user string, event *DataEvent) error {
	for {
		time.Sleep(300 * time.Millisecond)

		br := false
		var err error

		func() {
			cm.rwMutex.RLock()
			defer cm.rwMutex.RUnlock()

			if cm.subCount[user] == 0 {
				br = true
				err = ErrNoConsumers
				return
			}

			select {
			case cm.dataStreams[user] <- event:
				br = true
			default:
			}
		}()

		if br {
			return err
		}
	}
}

func (cm *ConnectionManager) NotifyUpdateWhitelist(user *db.Human) {
	cm.rwMutex.Lock()
	defer cm.rwMutex.Unlock()

	cm.HandleUser(user)
}

func (cm *ConnectionManager) NotifyUpdateSettings(login string) {
	cm.rwMutex.Lock()
	defer cm.rwMutex.Unlock()

	if ch, ok := cm.updateEventsCh[login]; ok {
		ch <- struct{}{}
	}
}

func (cm *ConnectionManager) HandleUser(user *db.Human) {
	if user.BannedBy != nil {
		cm.slog.Info("stopping processor", "user", user.Login)

		if ch, ok := cm.updateEventsCh[user.Login]; ok {
			close(ch)
			delete(cm.updateEventsCh, user.Login)
		}
	} else if _, ok := cm.updateEventsCh[user.Login]; !ok {
		cm.updateEventsCh[user.Login] = make(chan struct{})

		cm.slog.Info("starting processor", "user", user.Login)

		cm.wg.Add(1)
		go func() {
			defer cm.wg.Done()

		loop:
			for {
				select {
				case <-cm.ctx.Done():
					break loop
				default:
				}

				if err := cm.processor(user.Login); err != nil {
					cm.slog.Error("failed to create processor", "err", err, "user", user.Login)
				}
			}
		}()
	}
}

func (cm *ConnectionManager) ProcessingLoop() error {
	users, err := db.GetDbWhitelist()
	if err != nil {
		return fmt.Errorf("failed to get whitelist: %w", err)
	}

	cm.slog.Info("got users from db", "users", users.List)

	func() {
		cm.rwMutex.Lock()
		defer cm.rwMutex.Unlock()

		for _, user := range users.List {
			cm.HandleUser(user)
		}
	}()

	cm.wg.Wait()

	return nil
}
