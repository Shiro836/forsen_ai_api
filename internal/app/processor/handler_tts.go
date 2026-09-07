package processor

import (
	"context"
	"fmt"
	"log/slog"

	"app/db"
	"app/internal/app/conns"
	"app/pkg/textfilter"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"

	"app/internal/app/monitoring"
)

type TTSHandler struct {
	logger  *slog.Logger
	db      *db.DB
	service *Service
}

func NewTTSHandler(logger *slog.Logger, db *db.DB, service *Service) *TTSHandler {
	return &TTSHandler{
		logger:  logger,
		db:      db,
		service: service,
	}
}

func (h *TTSHandler) Handle(ctx context.Context, input InteractionInput, eventWriter conns.EventWriter) error {
	logger := h.logger.With("handler", "TTS", "requester", input.Requester, "user", input.Broadcaster.TwitchLogin)

	timer := prometheus.NewTimer(monitoring.AppMetrics.TTSQueryTime)
	defer timer.ObserveDuration()

	if input.Character != nil {
		if err := h.db.IncrementCharTTSRedeems(ctx, input.Character.ID); err != nil {
			logger.Warn("failed to increment tts_redeems", "err", err)
		}
	}

	msgID, err := uuid.Parse(input.MsgID)
	if err != nil {
		return fmt.Errorf("invalid msg id: %w", err)
	}

	eventWriter(&conns.DataEvent{
		EventType: conns.EventTypeImage,
		EventData: []byte("/characters/" + input.Character.ID.String() + "/image"),
	})

	skipLLMFilter := input.SkipLLMFilterFully || input.UserSettings.DisableLLMFilter

	spoken, requestMap := spokenRequest("", input.Message)
	requestRun, err := h.service.filterSpans(ctx, input.UserSettings, spoken, skipLLMFilter)
	if err != nil {
		return fmt.Errorf("failed to filter request: %w", err)
	}
	requestSpans := requestRun.Spans()
	filteredRequest := textfilter.Censor(spoken, requestSpans, "(filtered)")
	recordFilter(ctx, requestRun, requestMap)

	if requestSpans := requestMap.MapBack(requestSpans); len(requestSpans) > 0 {
		if err := h.db.UpdateMessageData(ctx, msgID, &db.MessageData{RequestFiltered: requestSpans}); err != nil {
			logger.Warn("failed to store filtered spans", "err", err)
		}
		h.service.connManager.NotifyControlPanel(input.Broadcaster.ID)
	}

	if input.State.IsSkipped(msgID) {
		return nil
	}

	requestTtsDone, err := h.service.playTTSStreaming(ctx, logger, eventWriter, input.AudioWriter, filteredRequest, msgID, input.Character.Data.VoiceReference, input.State, input.UserSettings, nil)
	if err != nil {
		return err
	}

	select {
	case <-requestTtsDone:
	case <-ctx.Done():
		return nil
	}

	eventWriter(cleanEvent())

	eventWriter(&conns.DataEvent{
		EventType: conns.EventTypeImage,
		EventData: []byte(" "),
	})

	return nil
}
