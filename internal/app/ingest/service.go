package ingest

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"app/db"
	"app/pkg/imagetag"
	"app/pkg/twitch"

	gempir "github.com/gempir/go-twitch-irc/v4"
	"github.com/google/uuid"
)

type Service struct {
	logger *slog.Logger
	db     *db.DB
	cfg    *twitch.Config

	chatClient *twitch.ShardedClient

	activeUsers     map[string]uuid.UUID
	activeUsersLock sync.RWMutex
}

func NewService(logger *slog.Logger, database *db.DB, cfg *twitch.Config) *Service {
	s := &Service{
		logger:      logger,
		db:          database,
		cfg:         cfg,
		activeUsers: make(map[string]uuid.UUID),
	}

	s.chatClient = twitch.NewShardedClient(
		logger,
		s.handleMessage,
		func() { metrics.ConnectedClients.Inc() },
		func() { metrics.ConnectedClients.Dec() },
		func(channel, reason string) {
			metrics.JoinFailures.WithLabelValues(reason).Inc()
			s.logger.Warn("twitch channel join failed", "channel", channel, "reason", reason)
		},
	)

	return s
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

	s.chatClient.Close()

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
		desiredUsers[strings.ToLower(u.TwitchLogin)] = u.ID
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

	metrics.ActiveChannels.Set(float64(len(s.activeUsers)))
	metrics.ShardCount.Set(float64(s.chatClient.ShardCount()))
	metrics.JoinedChannels.Set(float64(s.chatClient.JoinedChannelCount()))

	return nil
}

func (s *Service) joinChannel(channel string) error {
	s.chatClient.Join(channel)
	return nil
}

func (s *Service) departChannel(channel string) {
	s.chatClient.Depart(channel)
}

func (s *Service) handleMessage(msg gempir.PrivateMessage) {
	metrics.MessagesIngested.Inc()

	s.activeUsersLock.RLock()
	userID, ok := s.activeUsers[strings.ToLower(msg.Channel)]
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

	imageIDs := imagetag.ExtractIDs(msg.Message, 2)

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
