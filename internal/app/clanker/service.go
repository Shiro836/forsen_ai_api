package clanker

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"app/cfg"
	"app/db"
	"app/pkg/llm"

	"github.com/nicklaw5/helix/v2"
)

const numWorkers = 3

type Service struct {
	logger *slog.Logger
	db     *db.DB
	llm    *llm.Client
	cfg    *cfg.ClankerConfig

	helix *helix.Client
	jobs  chan *db.ClankerMessage
	wg    sync.WaitGroup
}

func NewService(logger *slog.Logger, database *db.DB, llmClient *llm.Client, clankerCfg *cfg.ClankerConfig) (*Service, error) {
	httpClient := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			IdleConnTimeout:   90 * time.Second,
			DisableKeepAlives: false,
			DialContext: (&net.Dialer{
				Timeout: 10 * time.Second,
			}).DialContext,
			ForceAttemptHTTP2: true,
		},
	}

	helixClient, err := helix.NewClient(&helix.Options{
		ClientID:     clankerCfg.ClientID,
		ClientSecret: clankerCfg.ClientSecret,
		RefreshToken: clankerCfg.RefreshToken,
		HTTPClient:   httpClient,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create helix client: %w", err)
	}

	// get a fresh access token on startup using the refresh token
	refreshResp, err := helixClient.RefreshUserAccessToken(clankerCfg.RefreshToken)
	if err != nil {
		return nil, fmt.Errorf("failed to refresh access token: %w", err)
	}
	if refreshResp.StatusCode != 200 {
		return nil, fmt.Errorf("failed to refresh access token: status %d, error: %s", refreshResp.StatusCode, refreshResp.ErrorMessage)
	}

	helixClient.SetUserAccessToken(refreshResp.Data.AccessToken)
	helixClient.SetRefreshToken(refreshResp.Data.RefreshToken)
	logger.Info("refreshed twitch access token on startup")

	return &Service{
		logger: logger,
		db:     database,
		llm:    llmClient,
		cfg:    clankerCfg,
		helix:  helixClient,
		jobs:   make(chan *db.ClankerMessage, numWorkers),
	}, nil
}

func (s *Service) Run(ctx context.Context) error {
	s.logger.Info("starting clanker service")

	// start workers
	for i := range numWorkers {
		s.wg.Add(1)
		go s.worker(ctx, i)
	}

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	cleanTicker := time.NewTicker(5 * time.Minute)
	defer cleanTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			s.logger.Info("stopping clanker service")
			close(s.jobs)
			s.wg.Wait()
			return nil
		case <-cleanTicker.C:
			if err := s.db.CleanClankerQueue(ctx); err != nil {
				s.logger.Error("failed to clean clanker queue", "err", err)
			}
		case <-ticker.C:
			s.dispatch(ctx)
		}
	}
}

func (s *Service) dispatch(ctx context.Context) {
	msg, err := s.db.GetNextClankerMsg(ctx)
	if err != nil {
		if db.ErrCode(err) != db.ErrCodeNoRows {
			s.logger.Error("failed to get next clanker message", "err", err)
		}
		return
	}

	select {
	case s.jobs <- msg:
	case <-ctx.Done():
	}
}

func (s *Service) worker(ctx context.Context, id int) {
	defer s.wg.Done()
	s.logger.Info("clanker worker started", "worker", id)

	for msg := range s.jobs {
		s.processMessage(ctx, msg)
	}

	s.logger.Info("clanker worker stopped", "worker", id)
}

func (s *Service) processMessage(ctx context.Context, msg *db.ClankerMessage) {
	logger := s.logger.With("msg_id", msg.ID, "channel", msg.ChannelLogin, "sender", msg.SenderLogin)
	logger.Info("processing clanker message", "message", msg.Message)

	command, args := parseCommand(msg.Message)

	var handleErr error
	switch command {
	case "gpt":
		handleErr = s.handleGPT(ctx, logger, msg, args)
	default:
		logger.Warn("unknown clanker command", "command", command)
	}

	if handleErr != nil {
		logger.Error("failed to handle clanker command", "command", command, "err", handleErr)
	}

	if err := s.db.UpdateClankerMsgStatus(ctx, msg.ID, db.ClankerStatusProcessed); err != nil {
		logger.Error("failed to update clanker message status", "err", err)
	}
}

func (s *Service) handleGPT(ctx context.Context, logger *slog.Logger, msg *db.ClankerMessage, prompt string) error {
	if len(prompt) == 0 {
		return nil
	}

	response, err := s.askWithTools(ctx, logger, prompt)
	if err != nil {
		return fmt.Errorf("failed to ask llm: %w", err)
	}

	response = strings.TrimSpace(response)
	if len(response) == 0 {
		logger.Warn("empty llm response")
		return nil
	}

	parts := splitMessage(response, 480)
	broadcasterID := strconv.Itoa(msg.ChannelUserID)

	for i, part := range parts {
		if i > 0 {
			time.Sleep(300 * time.Millisecond)
		}

		if err := s.sendChatMessage(broadcasterID, part); err != nil {
			return fmt.Errorf("failed to send chat message (part %d): %w", i+1, err)
		}

		logger.Info("sent chat message", "part", i+1, "total", len(parts))
	}

	return nil
}

func (s *Service) sendMessage(broadcasterID, message string) error {
	return s.sendChatMessage(broadcasterID, message)
}

// sendChatMessage sends a chat message with one retry on transient errors (stale HTTP/2 connections, 5xx).
func (s *Service) sendChatMessage(broadcasterID, message string) error {
	params := &helix.SendChatMessageParams{
		BroadcasterID: broadcasterID,
		SenderID:      s.cfg.BotUserID,
		Message:       message,
	}

	resp, err := s.helix.SendChatMessage(params)
	if err == nil && resp.StatusCode < 400 {
		return nil
	}

	// retry once after a short pause
	s.logger.Warn("chat message failed, retrying", "err", err, "status", respStatus(resp))
	time.Sleep(1 * time.Second)

	resp, err = s.helix.SendChatMessage(params)
	if err != nil {
		return fmt.Errorf("failed to send chat message: %w", err)
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("failed to send chat message: status %d, error: %s", resp.StatusCode, resp.ErrorMessage)
	}
	return nil
}

func respStatus(resp *helix.ChatMessageResponse) int {
	if resp == nil {
		return 0
	}
	return resp.StatusCode
}

// parseCommand splits "^^gpt hello world" into ("gpt", "hello world")
func parseCommand(message string) (string, string) {
	message = strings.TrimPrefix(message, "^^")
	parts := strings.SplitN(message, " ", 2)
	command := strings.ToLower(parts[0])
	args := ""
	if len(parts) > 1 {
		args = strings.TrimSpace(parts[1])
	}
	return command, args
}

// splitMessage splits a message into chunks that fit within Twitch's character limit
func splitMessage(message string, maxLen int) []string {
	if len(message) <= maxLen {
		return []string{message}
	}

	var parts []string
	for len(message) > 0 {
		if len(message) <= maxLen {
			parts = append(parts, message)
			break
		}

		// Try to split at a space
		cutAt := maxLen
		if idx := strings.LastIndex(message[:maxLen], " "); idx > maxLen/2 {
			cutAt = idx
		}

		parts = append(parts, message[:cutAt])
		message = strings.TrimSpace(message[cutAt:])
	}

	return parts
}
