package processor

import (
	"context"

	"app/db"
	"app/internal/app/conns"
	"app/pkg/llm"
	"app/pkg/whisperx"
)

// InteractionHandler defines the interface for handling different types of interactions (AI, TTS, Universal).
type InteractionHandler interface {
	Handle(ctx context.Context, input InteractionInput, output conns.EventWriter) error
}

// InteractionInput contains all the necessary data for an interaction.
type InteractionInput struct {
	Requester    string
	Message      string
	Character    *db.Card
	UserSettings *db.UserSettings
	MsgID        string // UUID as string

	State *ProcessorState
}

// LLMClient defines the interface for LLM interactions.
type LLMClient interface {
	Ask(ctx context.Context, prompt string) (string, error)
	AskMessages(ctx context.Context, messages []llm.Message, attachments []llm.Attachment) (string, error)
}

// TTSClient defines the interface for TTS interactions.
type TTSClient interface {
	TTS(ctx context.Context, text string, refAudio []byte) ([]byte, []whisperx.Timiing, error)
}
