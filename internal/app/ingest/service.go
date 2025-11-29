package ingest

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"sync"
	"time"

	"app/db"
	"app/pkg/twitch"

	gempir "github.com/gempir/go-twitch-irc/v4"
	"github.com/google/uuid"
)

const maxChannelsPerClient = 50

type ClientWrapper struct {
	client   *gempir.Client
	channels map[string]bool
	lock     sync.Mutex
	id       int
}

type Service struct {
	logger *slog.Logger
	db     *db.DB
	cfg    *twitch.Config

	clients     []*ClientWrapper
	clientsLock sync.RWMutex

	activeUsers     map[string]uuid.UUID
	activeUsersLock sync.RWMutex
}

func NewService(logger *slog.Logger, database *db.DB, cfg *twitch.Config) *Service {
	return &Service{
		logger:      logger,
		db:          database,
		cfg:         cfg,
		clients:     make([]*ClientWrapper, 0),
		activeUsers: make(map[string]uuid.UUID),
	}
}

func (s *Service) createNewClient(id int) *ClientWrapper {
	client := gempir.NewAnonymousClient()
	wrapper := &ClientWrapper{
		client:   client,
		channels: make(map[string]bool),
		id:       id,
	}

	client.OnPrivateMessage(func(msg gempir.PrivateMessage) {
		metrics.MessagesIngested.Inc()
		s.handleMessage(msg)
	})

	client.OnConnect(func() {
		s.logger.Info("twitch client connected", "client_id", id)
		metrics.ConnectedClients.Inc()
	})

	return wrapper
}

func (s *Service) Run(ctx context.Context) error {
	s.logger.Info("starting ingest service")

	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		s.pollUsers(ctx)
	}()

	<-ctx.Done()
	s.logger.Info("stopping ingest service")

	s.clientsLock.Lock()
	for _, wrapper := range s.clients {
		if err := wrapper.client.Disconnect(); err != nil {
			s.logger.Error("failed to disconnect twitch client", "err", err, "client_id", wrapper.id)
		}
	}
	s.clientsLock.Unlock()

	wg.Wait()
	return nil
}

func (s *Service) pollUsers(ctx context.Context) {
	if err := s.syncUsers(ctx); err != nil {
		s.logger.Error("failed to sync users", "err", err)
	}

	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.syncUsers(ctx); err != nil {
				s.logger.Error("failed to sync users", "err", err)
			}
		}
	}
}

func (s *Service) syncUsers(ctx context.Context) error {
	users, err := s.db.GetUsersPermissions(ctx, db.PermissionStreamer, db.PermissionStatusGranted)
	if err != nil {
		return fmt.Errorf("failed to get users: %w", err)
	}

	metrics.TotalGrantedChannels.Set(float64(len(users)))

	desiredUsers := make(map[string]uuid.UUID)
	for _, u := range users {
		desiredUsers[u.TwitchLogin] = u.ID
	}

	s.activeUsersLock.Lock()
	defer s.activeUsersLock.Unlock()

	for login, id := range desiredUsers {
		if _, ok := s.activeUsers[login]; !ok {
			s.logger.Info("joining channel", "login", login)
			if err := s.joinChannel(login); err != nil {
				s.logger.Error("failed to join channel", "login", login, "err", err)
				continue
			}
			s.activeUsers[login] = id
		}
	}

	for login := range s.activeUsers {
		if _, ok := desiredUsers[login]; !ok {
			s.logger.Info("departing channel", "login", login)
			s.departChannel(login)
			delete(s.activeUsers, login)
		}
	}

	return nil
}

func (s *Service) joinChannel(channel string) error {
	s.clientsLock.Lock()
	defer s.clientsLock.Unlock()

	for _, wrapper := range s.clients {
		wrapper.lock.Lock()
		if len(wrapper.channels) < maxChannelsPerClient {
			wrapper.client.Join(channel)
			wrapper.channels[channel] = true
			wrapper.lock.Unlock()
			metrics.ActiveChannels.Inc()
			return nil
		}
		wrapper.lock.Unlock()
	}

	newClientID := len(s.clients)
	newWrapper := s.createNewClient(newClientID)

	go func(w *ClientWrapper) {
		if err := w.client.Connect(); err != nil {
			s.logger.Error("twitch client disconnected unexpectedly", "err", err, "client_id", w.id)
		}
		metrics.ConnectedClients.Dec()
	}(newWrapper)

	newWrapper.lock.Lock()
	newWrapper.client.Join(channel)
	newWrapper.channels[channel] = true
	newWrapper.lock.Unlock()
	metrics.ActiveChannels.Inc()

	s.clients = append(s.clients, newWrapper)
	s.logger.Info("spawned new twitch client", "client_id", newClientID)

	return nil
}

func (s *Service) departChannel(channel string) {
	s.clientsLock.RLock()
	defer s.clientsLock.RUnlock()

	for _, wrapper := range s.clients {
		wrapper.lock.Lock()
		if _, ok := wrapper.channels[channel]; ok {
			wrapper.client.Depart(channel)
			delete(wrapper.channels, channel)
			wrapper.lock.Unlock()
			metrics.ActiveChannels.Dec()
			return
		}
		wrapper.lock.Unlock()
	}
}

var imgRegex = regexp.MustCompile(`<img:([A-Za-z0-9]{5})>`)

func (s *Service) handleMessage(msg gempir.PrivateMessage) {
	s.activeUsersLock.RLock()
	userID, ok := s.activeUsers[msg.Channel] // msg.Channel is the login
	s.activeUsersLock.RUnlock()

	if !ok {
		return
	}

	if len(msg.Message) == 0 || len(msg.User.Name) == 0 {
		return
	}

	if len(msg.CustomRewardID) == 0 {
		return
	}

	imgMatches := imgRegex.FindAllStringSubmatch(msg.Message, -1)
	imageIDs := make([]string, 0, 2)
	for _, m := range imgMatches {
		if len(m) >= 2 {
			imageIDs = append(imageIDs, m[1])
			if len(imageIDs) == 2 {
				break
			}
		}
	}

	showImages := false
	data := &db.MessageData{
		ImageIDs:   imageIDs,
		ShowImages: &showImages,
	}

	twitchMsg := db.TwitchMessage{
		TwitchLogin: msg.User.Name,
		Message:     msg.Message,
		RewardID:    msg.CustomRewardID,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := s.db.PushIngestMsg(ctx, userID, twitchMsg, data, msg.ID)
	if err != nil {
		s.logger.Error("failed to push message", "err", err, "user", msg.Channel)
	} else {
		s.logger.Info("ingested message", "user", msg.Channel, "msg_id", msg.ID)
	}
}
