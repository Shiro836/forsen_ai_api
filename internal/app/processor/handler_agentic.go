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
	llmModel CharacterLLM
	service  *Service
}

func NewAgenticHandler(logger *slog.Logger, db *db.DB, detector *agentic.Detector, planner *agentic.Planner, llmModel CharacterLLM, service *Service) *AgenticHandler {
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

	firstSpeakerName, err := h.planner.SelectFirstSpeaker(ctx, input.Message, charCardsSlice)
	if err != nil {
		logger.Error("failed to select first speaker", "err", err)
		return fmt.Errorf("failed to select first speaker: %w", err)
	}

	firstSpeakerID, ok := nameToID[strings.ToLower(firstSpeakerName)]
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
		return fmt.Errorf("character card not found for %s", firstSpeakerName)
	}

	firstResponse, err := h.llmModel.DialogueReply(ctx, firstCard, input.Message)
	if err != nil {
		logger.Error("failed to generate first dialogue response", "err", err)
		return fmt.Errorf("failed to generate first dialogue response: %w", err)
	}

	firstMsg := stripLeadingSpeakerPrefix(firstCard.Name, firstResponse)
	appendHistoryTurn(&history, firstCard.Name, firstMsg)

	curText := h.service.FilterText(ctx, input.UserSettings, firstMsg)
	curCard := firstCard
	var prevDone <-chan struct{}

	for turn := 0; turn < MaxAgenticTurns; turn++ {
		if input.State.IsSkipped(msgUUID) {
			return nil
		}

		var gate chan struct{}
		if prevDone == nil {
			eventWriter(characterImageEvent(curCard.ID))
		} else {
			// the portrait must not switch while the previous turn still plays
			gate = make(chan struct{})
			go func(prev <-chan struct{}, cardID uuid.UUID) {
				defer close(gate)
				select {
				case <-prev:
					eventWriter(characterImageEvent(cardID))
				case <-ctx.Done():
				}
			}(prevDone, curCard.ID)
		}

		done, err := h.service.playTTSStreaming(ctx, logger, eventWriter, input.AudioWriter, curText, msgUUID, curCard.Data.VoiceReference, input.State, input.UserSettings, gate)
		if err != nil {
			logger.Error("failed to play TTS", "err", err)
			if prevDone != nil {
				select {
				case <-prevDone:
				case <-ctx.Done():
				}
			}
			return nil
		}

		// next turn's LLM call and gated synthesis run while this turn plays
		curText, curCard = "", nil
		if turn+1 < MaxAgenticTurns {
			nextText, nextCard, err := h.prepareNextAgenticTurnText(ctx, input.Message, &history, charNames, charCards, nameToID, input.UserSettings)
			if err != nil {
				logger.Error("failed to prepare next agentic turn", "err", err)
			} else {
				curText, curCard = nextText, nextCard
			}
		}

		if curCard == nil {
			select {
			case <-done:
			case <-ctx.Done():
			}
			return nil
		}

		prevDone = done
	}

	return nil
}

func characterImageEvent(cardID uuid.UUID) *conns.DataEvent {
	return &conns.DataEvent{
		EventType: conns.EventTypeImage,
		EventData: []byte(fmt.Sprintf("/characters/%s/image", cardID)),
	}
}

// prepareNextAgenticTurnText picks the next speaker and generates their
// filtered line; ("", nil, nil) means the planner ended the dialogue.
func (h *AgenticHandler) prepareNextAgenticTurnText(
	ctx context.Context,
	scenario string,
	history *[]llm.Message,
	charNames []string,
	charCards map[uuid.UUID]*db.Card,
	nameToID map[string]uuid.UUID,
	userSettings *db.UserSettings,
) (string, *db.Card, error) {
	nextSpeakerName, err := h.planner.SelectNextSpeaker(ctx, scenario, *history, charNames)
	if err != nil {
		return "", nil, fmt.Errorf("failed to select next speaker: %w", err)
	}

	if nextSpeakerName == "END" {
		return "", nil, nil
	}

	nextSpeakerID, ok := nameToID[strings.ToLower(nextSpeakerName)]
	if !ok {
		return "", nil, fmt.Errorf("planner returned unknown next speaker: %s", nextSpeakerName)
	}

	nextCard, ok := charCards[nextSpeakerID]
	if !ok {
		return "", nil, fmt.Errorf("character card not found for speaker %s", nextSpeakerName)
	}

	response, err := h.llmModel.DialogueReply(ctx, nextCard, scenario, collectHistoryTurns(*history)...)
	if err != nil {
		return "", nil, fmt.Errorf("failed to generate response: %w", err)
	}

	cleanResponse := stripLeadingSpeakerPrefix(nextCard.Name, response)
	appendHistoryTurn(history, nextCard.Name, cleanResponse)

	return h.service.FilterText(ctx, userSettings, cleanResponse), nextCard, nil
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

func stripCI(prefix, msg string) string {
	if prefix == "" || msg == "" {
		return msg
	}

	trimmed := strings.TrimLeft(msg, " \t\r\n")
	if len(trimmed) < len(prefix) {
		return msg
	}

	if !strings.EqualFold(trimmed[:len(prefix)], prefix) {
		return msg
	}

	return strings.TrimLeft(trimmed[len(prefix):], " \t\r\n")
}

func stripLeadingSpeakerPrefix(speaker, msg string) string {
	speaker = strings.TrimSpace(speaker)
	if speaker == "" || msg == "" {
		return msg
	}

	msg = stripCI(speaker+":", msg)
	msg = stripCI(speaker+" :", msg)

	return msg
}
