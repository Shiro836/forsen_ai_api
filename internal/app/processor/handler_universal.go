package processor

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"app/db"
	"app/internal/app/conns"
	"app/pkg/imagetag"
	"app/pkg/textfilter"

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
	logger := h.logger.With("handler", "Universal", "requester", input.Requester, "user", input.Broadcaster.TwitchLogin)

	timer := prometheus.NewTimer(monitoring.AppMetrics.UniversalQueryTime)
	defer timer.ObserveDuration()

	msgID, err := uuid.Parse(input.MsgID)
	if err != nil {
		return fmt.Errorf("invalid msg id: %w", err)
	}

	skipLLMFilter := input.SkipLLMFilterFully || input.UserSettings.DisableLLMFilter

	// Filter the raw message (not the image-tag-replaced one) so the spans line
	// up with what the control panel displays; image tags survive censoring
	// (disjoint spans) and are replaced afterward for speech.
	requestSpans, err := h.service.filterSpans(ctx, input.UserSettings, input.Message, skipLLMFilter)
	if err != nil {
		return fmt.Errorf("failed to filter request: %w", err)
	}
	filteredRequest := imagetag.ReplaceImageTags(textfilter.Censor(input.Message, requestSpans, "(filtered)"))

	if len(requestSpans) > 0 {
		if err := h.db.UpdateMessageData(ctx, msgID, &db.MessageData{RequestFiltered: requestSpans}); err != nil {
			logger.Warn("failed to store filtered spans", "err", err)
		}
		h.service.connManager.NotifyControlPanel(input.Broadcaster.ID)
	}

	if input.State.IsSkipped(msgID) {
		return nil
	}

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

	requestTtsDone, err := h.service.playUniversalTTS(ctx, logger, eventWriter, input.AudioWriter, actions, msgID, input.State, input.UserSettings)
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
