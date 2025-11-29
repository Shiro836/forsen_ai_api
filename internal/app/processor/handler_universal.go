package processor

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"app/db"
	"app/internal/app/conns"

	"github.com/prometheus/client_golang/prometheus"

	"app/internal/app/monitoring"

	"github.com/google/uuid"
)

type UniversalHandler struct {
	logger  *slog.Logger
	db      *db.DB
	service *Service
}

func NewUniversalHandler(logger *slog.Logger, db *db.DB, service *Service) *UniversalHandler {
	return &UniversalHandler{
		logger:  logger,
		db:      db,
		service: service,
	}
}

func (h *UniversalHandler) Handle(ctx context.Context, input InteractionInput, eventWriter conns.EventWriter) error {
	logger := h.logger.With("handler", "Universal", "requester", input.Requester)

	timer := prometheus.NewTimer(monitoring.AppMetrics.UniversalQueryTime)
	defer timer.ObserveDuration()

	msgID, err := uuid.Parse(input.MsgID)
	if err != nil {
		return fmt.Errorf("invalid msg id: %w", err)
	}

	ttsMsg := replaceImageTagsForTTS(input.Message)
	filteredRequest := h.service.FilterText(ctx, input.UserSettings, ttsMsg)

	actions, err := h.service.processUniversalTTSMessage(ctx, filteredRequest, input.UserSettings)
	if err != nil {
		return err
	}

	// Increment TTS redeems once per unique referenced voice
	uniqueVoiceIDs := make(map[uuid.UUID]struct{})
	for _, action := range actions {
		if strings.TrimSpace(action.Text) == "" {
			continue
		}
		voice := action.Voice
		if voice == "" {
			voice = "obiwan"
		}
		if voiceID, _, vErr := h.service.getVoiceReference(ctx, logger, voice); vErr == nil {
			uniqueVoiceIDs[voiceID] = struct{}{}
		} else {
			logger.Debug("voice not found for increment", "voice", voice, "err", vErr)
		}
	}
	for voiceID := range uniqueVoiceIDs {
		if err := h.db.IncrementCharTTSRedeems(ctx, voiceID); err != nil {
			logger.Warn("failed to increment universal tts redeems", "voice_id", voiceID, "err", err)
		}
	}

	if input.State.IsSkipped(msgID) {
		return nil
	}

	requestTtsDone, err := h.service.playUniversalTTS(ctx, logger, eventWriter, actions, msgID, input.State, input.UserSettings)
	if err != nil {
		return err
	}

	select {
	case <-requestTtsDone:
	case <-ctx.Done():
		return nil
	}

	eventWriter(&conns.DataEvent{
		EventType: conns.EventTypeText,
		EventData: []byte(" "),
	})

	eventWriter(&conns.DataEvent{
		EventType: conns.EventTypeImage,
		EventData: []byte(" "),
	})

	return nil
}
