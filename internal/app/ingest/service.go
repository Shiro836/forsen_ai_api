package ingest

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"app/db"
	"app/pkg/imagetag"
	"app/pkg/twitch"

	gempir "github.com/gempir/go-twitch-irc/v4"
	"github.com/google/uuid"
)

type ingestUserConfig struct {
	id                uuid.UUID
	twitchUserID      int
	ingestAllMessages bool
}

type Service struct {
	logger *slog.Logger
	db     *db.DB
	cfg    *twitch.Config

	chatClient *twitch.ShardedClient

	activeUsers     map[string]*ingestUserConfig
	activeUsersLock sync.RWMutex
}

func NewService(logger *slog.Logger, database *db.DB, cfg *twitch.Config) *Service {
	s := &Service{
		logger:      logger,
		db:          database,
		cfg:         cfg,
		activeUsers: make(map[string]*ingestUserConfig),
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

	ticker := time.NewTicker(10 * time.Second)
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
	users, err := s.db.GetIngestUsers(ctx)
	if err != nil {
		return fmt.Errorf("failed to get users: %w", err)
	}

	metrics.TotalGrantedChannels.Set(float64(len(users)))

	desiredUsers := make(map[string]*ingestUserConfig, len(users))
	for _, u := range users {
		desiredUsers[strings.ToLower(u.TwitchLogin)] = &ingestUserConfig{
			id:                u.ID,
			twitchUserID:      u.TwitchUserID,
			ingestAllMessages: u.IngestAllMessages,
		}
	}

	s.activeUsersLock.Lock()
	defer s.activeUsersLock.Unlock()

	for login, cfg := range desiredUsers {
		if existing, ok := s.activeUsers[login]; !ok {
			s.logger.Info("joining channel", "login", login)
			if err := s.joinChannel(login); err != nil {
				s.logger.Error("failed to join channel", "login", login, "err", err)
				continue
			}
			s.activeUsers[login] = cfg
		} else {
			existing.ingestAllMessages = cfg.ingestAllMessages
			existing.twitchUserID = cfg.twitchUserID
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
	userCfg, ok := s.activeUsers[strings.ToLower(msg.Channel)]
	s.activeUsersLock.RUnlock()

	if !ok {
		return
	}

	if len(msg.Message) == 0 || len(msg.User.Name) == 0 {
		return
	}

	twitchUserID, _ := strconv.Atoi(msg.User.ID)

	// Handle ^^voice command before any other processing
	if voiceName, ok := parseVoiceCommand(msg.Message); ok {
		s.handleVoiceCommand(twitchUserID, msg.User.Name, voiceName)
		return
	}

	// Route ^^ commands (except ^^voice) to clanker queue
	if strings.HasPrefix(msg.Message, "^^") {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		_, err := s.db.PushClankerMsg(ctx, msg.Channel, userCfg.twitchUserID, msg.User.Name, twitchUserID, msg.Message, msg.ID)
		if err != nil {
			s.logger.Error("failed to push clanker message", "err", err, "user", msg.Channel)
		} else {
			s.logger.Info("routed clanker message", "user", msg.Channel, "msg_id", msg.ID)
		}
		return
	}

	if len(msg.CustomRewardID) == 0 && !userCfg.ingestAllMessages {
		return
	}

	imageIDs := imagetag.ExtractIDs(msg.Message, 2)

	showImages := false
	data := &db.MessageData{
		ImageIDs:   imageIDs,
		ShowImages: &showImages,
	}

	twitchMsg := db.TwitchMessage{
		TwitchLogin:  msg.User.Name,
		TwitchUserID: twitchUserID,
		Message:      msg.Message,
		RewardID:     msg.CustomRewardID,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := s.db.PushIngestMsg(ctx, userCfg.id, twitchMsg, data, msg.ID)
	if err != nil {
		s.logger.Error("failed to push message", "err", err, "user", msg.Channel)
	} else {
		s.logger.Info("ingested message", "user", msg.Channel, "msg_id", msg.ID)
	}
}

func parseVoiceCommand(message string) (string, bool) {
	if !strings.HasPrefix(message, "^^voice ") {
		return "", false
	}

	voiceName := strings.TrimPrefix(message, "^^voice ")
	voiceName = strings.Trim(voiceName, "\" ")

	if len(voiceName) == 0 {
		return "", false
	}

	return voiceName, true
}

func (s *Service) handleVoiceCommand(twitchUserID int, twitchLogin, voiceName string) {
	if twitchUserID == 0 {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	exists, err := s.db.VoiceShortNameExists(ctx, voiceName)
	if err != nil {
		s.logger.Error("failed to check voice existence", "err", err, "voice", voiceName)
		return
	}

	if !exists {
		s.logger.Info("voice not found", "voice", voiceName, "user", twitchLogin)
		return
	}

	if err := s.db.SetChatUserVoice(ctx, twitchUserID, twitchLogin, voiceName); err != nil {
		s.logger.Error("failed to set chat user voice", "err", err, "user", twitchLogin, "voice", voiceName)
		return
	}

	s.logger.Info("voice set", "user", twitchLogin, "voice", voiceName)
}
