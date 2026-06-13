package processor

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode"

	"app/db"
	"app/internal/app/conns"
	"app/internal/app/monitoring"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
)

const defaultChatVoice = "tresh"

func filterASCII(s string) string {
	return strings.Map(func(r rune) rune {
		if r > unicode.MaxASCII {
			return -1
		}
		return r
	}, s)
}

type ChatTTSHandler struct {
	logger  *slog.Logger
	db      *db.DB
	service *Service
}

func NewChatTTSHandler(logger *slog.Logger, db *db.DB, service *Service) *ChatTTSHandler {
	return &ChatTTSHandler{
		logger:  logger,
		db:      db,
		service: service,
	}
}

func (h *ChatTTSHandler) Handle(ctx context.Context, input InteractionInput, eventWriter conns.EventWriter) error {
	logger := h.logger.With("handler", "ChatTTS", "requester", input.Requester, "user", input.Broadcaster.TwitchLogin)

	timer := prometheus.NewTimer(monitoring.AppMetrics.ChatTTSQueryTime)
	defer timer.ObserveDuration()

	msgID, err := uuid.Parse(input.MsgID)
	if err != nil {
		return fmt.Errorf("invalid msg id: %w", err)
	}

	asciiOnly := filterASCII(input.Message)
	filteredRequest := h.service.FilterText(ctx, input.UserSettings, asciiOnly)
	if len(filteredRequest) == 0 {
		return nil
	}

	// Poll for incoming reward messages — if one arrives, skip this chat TTS
	pollCtx, pollCancel := context.WithCancel(ctx)
	defer pollCancel()
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-pollCtx.Done():
				return
			case <-ticker.C:
				has, err := h.db.HasWaitingKnownRewardMessage(pollCtx, input.Broadcaster.ID)
				if err != nil {
					continue
				}
				if has {
					input.State.AddSkipped(msgID)
					eventWriter(&conns.DataEvent{
						EventType: conns.EventTypeSkip,
						EventData: []byte(msgID.String()),
					})
					return
				}
			}
		}
	}()

	voice := defaultChatVoice
	if input.TwitchUserID != 0 {
		if userVoice, err := h.db.GetChatUserVoice(ctx, input.TwitchUserID); err != nil {
			logger.Warn("failed to get chat user voice", "err", err)
		} else if len(userVoice) > 0 {
			voice = userVoice
		}
	}

	_, voiceRef, err := h.service.getVoiceReference(ctx, logger, voice)
	if err != nil {
		logger.Error("failed to get voice reference", "err", err, "voice", voice)
		return nil
	}

	requestAudio, textTimings, err := h.service.ChatTTSWithTimings(ctx, filteredRequest, voiceRef)
	if err != nil {
		logger.Error("chat TTS error", "err", err)
		return nil
	}

	if input.State.IsSkipped(msgID) {
		return nil
	}

	requestTtsDone, err := h.service.playTTS(ctx, logger, eventWriter, filteredRequest, msgID, requestAudio, textTimings, input.State, input.UserSettings)
	if err != nil {
		return err
	}

	select {
	case <-requestTtsDone:
	case <-ctx.Done():
		return nil
	}

	eventWriter(textEvent("", msgID))

	return nil
}
