package twitch

import (
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	gempir "github.com/gempir/go-twitch-irc/v4"
)

type Shard struct {
	client       *gempir.Client
	channels     map[string]bool
	lock         sync.RWMutex
	id           int
	logger       *slog.Logger
	stopping     atomic.Bool
	connected    atomic.Bool
	onDisconnect func()
}

func NewShard(id int, logger *slog.Logger, onMessage func(gempir.PrivateMessage), onConnect func(), onDisconnect func()) *Shard {
	client := gempir.NewAnonymousClient()
	s := &Shard{
		client:       client,
		channels:     make(map[string]bool),
		id:           id,
		logger:       logger,
		onDisconnect: onDisconnect,
	}

	client.OnPrivateMessage(onMessage)
	client.OnConnect(func() {
		s.connected.Store(true)
		onConnect()
	})

	return s
}

func (s *Shard) Join(channel string) {
	s.lock.Lock()
	defer s.lock.Unlock()

	if _, ok := s.channels[channel]; !ok {
		s.client.Join(channel)
		s.channels[channel] = true
	}
}

func (s *Shard) Depart(channel string) bool {
	s.lock.Lock()
	defer s.lock.Unlock()

	if _, ok := s.channels[channel]; ok {
		s.client.Depart(channel)
		delete(s.channels, channel)
		return true
	}
	return false
}

func (s *Shard) HasChannel(channel string) bool {
	s.lock.RLock()
	defer s.lock.RUnlock()
	_, ok := s.channels[channel]
	return ok
}

func (s *Shard) Count() int {
	s.lock.RLock()
	defer s.lock.RUnlock()
	return len(s.channels)
}

func (s *Shard) Connect() {
	go func() {
		for !s.stopping.Load() {
			s.logger.Info("connecting shard", "shard_id", s.id)
			err := s.client.Connect()

			if s.connected.CompareAndSwap(true, false) {
				s.onDisconnect()
			}

			if s.stopping.Load() {
				return
			}

			if err != nil {
				s.logger.Error("shard disconnected unexpectedly, retrying in 5s", "shard_id", s.id, "err", err)
				time.Sleep(5 * time.Second)
			} else {
				s.logger.Info("shard disconnected, retrying in 1s", "shard_id", s.id)
				time.Sleep(1 * time.Second)
			}
		}
	}()
}

func (s *Shard) Disconnect() {
	s.stopping.Store(true)
	if err := s.client.Disconnect(); err != nil {
		s.logger.Error("failed to disconnect shard", "shard_id", s.id, "err", err)
	}
}
