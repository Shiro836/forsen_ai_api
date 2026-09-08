package processor

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"app/db"
	"app/internal/app/conns"
	"app/pkg/textfilter"
	ttsprocessor "app/pkg/tts_processor"

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

	tokens := h.service.lexUniversal(ctx, input.UserSettings, input.Message)
	spoken, requestMap, ranges := spokenUniversal(input.Message, tokens)
	requestRun, err := h.service.filterSpans(ctx, input.UserSettings, spoken, skipLLMFilter)
	if err != nil {
		return fmt.Errorf("failed to filter request: %w", err)
	}
	requestSpans := requestRun.Spans()
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

	spokenRunes := []rune(spoken)
	actions := limitSfx(ttsprocessor.Actions(tokens, func(i int) string {
		r := ranges[i]
		return textfilter.Censor(string(spokenRunes[r.Start:r.End]), textfilter.Window(requestSpans, r.Start, r.End), "(filtered)")
	}), input.UserSettings)

	// Increment TTS redeems once per unique referenced voice
	uniqueVoiceIDs := make(map[uuid.UUID]struct{})
	for _, action := range actions {
		if strings.TrimSpace(action.Text) == "" {
			continue
		}
		voice := action.Voice
		if voice == "" {
			voice = DefaultUniversalVoice
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
