package processor

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"app/db"
	"app/internal/app/conns"
	ttsprocessor "app/pkg/tts_processor"
)

// UniversalAIHandler asks one character per message, picked with the same
// `name:` selector universal TTS uses for voices. Sound and filter tags stay
// literal: a universal AI message only picks who answers.
type UniversalAIHandler struct {
	logger  *slog.Logger
	db      *db.DB
	service *Service
	ai      InteractionHandler
}

func NewUniversalAIHandler(logger *slog.Logger, db *db.DB, service *Service, ai InteractionHandler) *UniversalAIHandler {
	return &UniversalAIHandler{
		logger:  logger,
		db:      db,
		service: service,
		ai:      ai,
	}
}

func (h *UniversalAIHandler) Handle(ctx context.Context, input InteractionInput, eventWriter conns.EventWriter) error {
	logger := h.logger.With("handler", "UniversalAI", "requester", input.Requester, "user", input.Broadcaster.TwitchLogin)

	name, question := selectUniversalAICharacter(input.Message, h.service.universalVoiceCheck(ctx, input.UserSettings))
	if name == "" {
		// an unnamed question goes to the same default character universal TTS
		// speaks unnamed spans with
		name = DefaultUniversalVoice
		logger.Info("no character selected, using default", "character", name)
	}

	id, _, err := h.db.GetVoiceReferenceByShortName(ctx, name)
	if err != nil {
		return fmt.Errorf("universal ai character %q: %w", name, err)
	}
	if input.UserSettings.CardDisabled(id) {
		return fmt.Errorf("universal ai character %q is disabled", name)
	}

	card, err := h.db.GetCharCardByID(ctx, input.Broadcaster.ID, id)
	if err != nil {
		return fmt.Errorf("universal ai load character %q: %w", name, err)
	}

	input.Character = card
	input.Message = question

	return h.ai.Handle(ctx, input, eventWriter)
}

// selectUniversalAICharacter returns the first `name:` that resolves to a
// character and the message with that selector removed. Later selectors and
// every other tag stay part of the question.
func selectUniversalAICharacter(msg string, checkVoice func(string) bool) (name, question string) {
	noTag := func(string) bool { return false }

	r := []rune(msg)
	for _, tok := range ttsprocessor.Lex(msg, checkVoice, noTag, noTag) {
		if tok.Kind != ttsprocessor.Voice {
			continue
		}
		left := strings.TrimRight(string(r[:tok.Start]), " \t")
		right := strings.TrimLeft(string(r[tok.End:]), " \t")
		return tok.Value, strings.TrimSpace(left + " " + right)
	}

	return "", strings.TrimSpace(msg)
}
