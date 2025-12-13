package processor

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"app/db"
	"app/internal/app/conns"
	"app/pkg/agentic"
	"app/pkg/llm"
	"app/pkg/whisperx"

	"github.com/prometheus/client_golang/prometheus"

	"app/internal/app/monitoring"

	"github.com/google/uuid"
)

const MaxAgenticTurns = 20

type AgenticHandler struct {
	logger   *slog.Logger
	db       *db.DB
	detector *agentic.Detector
	planner  *agentic.Planner
	llmModel LLMClient
	service  *Service
}

func NewAgenticHandler(logger *slog.Logger, db *db.DB, detector *agentic.Detector, planner *agentic.Planner, llmModel LLMClient, service *Service) *AgenticHandler {
	return &AgenticHandler{
		logger:   logger,
		db:       db,
		detector: detector,
		planner:  planner,
		llmModel: llmModel,
		service:  service,
	}
}

func (h *AgenticHandler) Handle(ctx context.Context, input InteractionInput, eventWriter conns.EventWriter) error {
	logger := h.logger.With("handler", "Agentic", "requester", input.Requester, "user", input.Broadcaster.TwitchLogin)

	timer := prometheus.NewTimer(monitoring.AppMetrics.AgenticQueryTime)
	defer timer.ObserveDuration()

	allChars, err := h.db.GetAllCharacterBasicInfo(ctx)
	if err != nil {
		return fmt.Errorf("failed to get all characters: %w", err)
	}

	detectedChars, err := h.detector.DetectCharacters(ctx, input.Message, allChars)
	if err != nil {
		logger.Error("failed to detect characters", "err", err)
		return fmt.Errorf("agentic flow failed: %w", err)
	}

	if len(detectedChars) == 0 {
		logger.Info("no characters detected in prompt")
		return nil
	}

	nameToID := make(map[string]uuid.UUID)
	charNames := make([]string, 0, len(detectedChars))
	charCards := make(map[uuid.UUID]*db.Card)

	for _, c := range detectedChars {
		nameToID[strings.ToLower(c.Name)] = c.ID
		charNames = append(charNames, c.Name)

		card, err := h.db.GetCharCardByID(ctx, uuid.Nil, c.ID)
		if err != nil {
			logger.Error("failed to prefetch character card", "id", c.ID, "name", c.Name, "err", err)
			continue
		}
		charCards[c.ID] = card
	}

	charCardsSlice := make([]*db.Card, 0, len(charCards))
	for _, card := range charCards {
		charCardsSlice = append(charCardsSlice, card)
	}

	initialPlan, err := h.planner.PlanInitialTurn(ctx, input.Message, charCardsSlice)
	if err != nil {
		logger.Error("failed to plan initial turn", "err", err)
		return fmt.Errorf("failed to plan initial turn: %w", err)
	}

	firstSpeakerID, ok := nameToID[strings.ToLower(initialPlan.FirstSpeakerName)]
	if !ok {
		return fmt.Errorf("no characters available for fallback")
	}

	history := []llm.Message{}

	msgUUID, err := uuid.Parse(input.MsgID)
	if err != nil {
		msgUUID = uuid.Nil
	}

	firstCard, ok := charCards[firstSpeakerID]
	if !ok {
		return fmt.Errorf("character card not found for %s", initialPlan.FirstSpeakerName)
	}

	appendHistoryTurn(&history, firstCard.Name, initialPlan.FirstMessageText)

	currentTurn, err := h.buildAgenticTurn(ctx, firstSpeakerID, firstCard, input.UserSettings, initialPlan.FirstMessageText)
	if err != nil {
		logger.Error("failed to prepare initial agentic turn", "err", err)
		return fmt.Errorf("failed to prepare initial agentic turn: %w", err)
	}

	for turn := 0; turn < MaxAgenticTurns && currentTurn != nil; turn++ {
		if input.State.IsSkipped(msgUUID) {
			return nil
		}

		eventWriter(&conns.DataEvent{
			EventType: conns.EventTypeImage,
			EventData: []byte(fmt.Sprintf("/characters/%s/image", currentTurn.card.ID)),
		})

		done, err := h.service.playTTS(ctx, logger, eventWriter, currentTurn.text, msgUUID, currentTurn.audio, currentTurn.timings, input.State, input.UserSettings)
		if err != nil {
			logger.Error("failed to play TTS", "err", err)
			break
		}

		var nextTurn *agenticTurn

		if turn+1 < MaxAgenticTurns {
			nextTurn, err = h.prepareNextAgenticTurn(ctx, input.Message, &history, charNames, charCards, nameToID, input.UserSettings)
			if err != nil {
				logger.Error("failed to prepare next agentic turn", "err", err)
				select {
				case <-done:
				case <-ctx.Done():
					return nil
				}
				break
			}
		}

		select {
		case <-done:
		case <-ctx.Done():
			return nil
		}

		if nextTurn == nil {
			break
		}

		currentTurn = nextTurn
	}

	return nil
}

type agenticTurn struct {
	speakerID uuid.UUID
	card      *db.Card
	text      string
	audio     []byte
	timings   []whisperx.Timiing
}

func (h *AgenticHandler) buildAgenticTurn(ctx context.Context, speakerID uuid.UUID, card *db.Card, userSettings *db.UserSettings, text string) (*agenticTurn, error) {
	if card == nil {
		return nil, fmt.Errorf("nil character card for speaker %s", speakerID)
	}

	filteredText := h.service.FilterText(ctx, userSettings, text)

	audio, timings, err := h.service.TTSWithTimings(ctx, filteredText, card.Data.VoiceReference)
	if err != nil {
		return nil, fmt.Errorf("failed to generate TTS for %s: %w", card.Name, err)
	}

	return &agenticTurn{
		speakerID: speakerID,
		card:      card,
		text:      filteredText,
		audio:     audio,
		timings:   timings,
	}, nil
}

func (h *AgenticHandler) prepareNextAgenticTurn(
	ctx context.Context,
	scenario string,
	history *[]llm.Message,
	charNames []string,
	charCards map[uuid.UUID]*db.Card,
	nameToID map[string]uuid.UUID,
	userSettings *db.UserSettings,
) (*agenticTurn, error) {
	nextSpeakerName, err := h.planner.SelectNextSpeaker(ctx, scenario, *history, charNames)
	if err != nil {
		return nil, fmt.Errorf("failed to select next speaker: %w", err)
	}

	if nextSpeakerName == "END" {
		return nil, nil
	}

	nextSpeakerID, ok := nameToID[strings.ToLower(nextSpeakerName)]
	if !ok {
		return nil, fmt.Errorf("planner returned unknown next speaker: %s", nextSpeakerName)
	}

	nextCard, ok := charCards[nextSpeakerID]
	if !ok {
		return nil, fmt.Errorf("character card not found for speaker %s", nextSpeakerName)
	}

	prompt, err := h.service.dialoguePrompt(nextCard, scenario, collectHistoryTurns(*history)...)
	if err != nil {
		return nil, fmt.Errorf("failed to craft prompt: %w", err)
	}

	response, err := h.llmModel.Ask(ctx, prompt)
	if err != nil {
		return nil, fmt.Errorf("failed to generate response: %w", err)
	}

	appendHistoryTurn(history, nextCard.Name, response)

	turn, err := h.buildAgenticTurn(ctx, nextSpeakerID, nextCard, userSettings, response)
	if err != nil {
		return nil, err
	}

	return turn, nil
}

func appendHistoryTurn(history *[]llm.Message, speakerName, text string) {
	if history == nil {
		return
	}

	*history = append(*history, llm.Message{
		Role: "user",
		Content: []llm.MessageContent{
			{
				Type: "text",
				Text: fmt.Sprintf("%s: %s", speakerName, text),
			},
		},
	})
}

func collectHistoryTurns(history []llm.Message) []string {
	var turns []string

	for _, msg := range history {
		for _, content := range msg.Content {
			if content.Type == "text" {
				turns = append(turns, content.Text)
			}
		}
	}

	return turns
}
