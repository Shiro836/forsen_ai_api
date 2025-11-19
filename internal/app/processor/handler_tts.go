package processor

import (
	"context"
	"fmt"
	"log/slog"

	"app/db"
	"app/internal/app/conns"

	"github.com/google/uuid"
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
	logger := h.logger.With("handler", "TTS", "requester", input.Requester)

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

	ttsMsg := replaceImageTagsForTTS(input.Message)
	filteredRequest := h.service.FilterText(ctx, input.UserSettings, ttsMsg)

	requestAudio, textTimings, err := h.service.TTSWithTimings(ctx, filteredRequest, input.Character.Data.VoiceReference)
	if err != nil {
		return err
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
