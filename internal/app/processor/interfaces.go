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
	TwitchUserID int
	Broadcaster  *db.User
	Message      string
	Character    *db.Card
	UserSettings *db.UserSettings
	MsgID        string // UUID as string

	SkipLLMFilterFully bool

	State *ProcessorState
}

type LLMClient interface {
	Ask(ctx context.Context, prompt string) (string, error)
	AskChat(ctx context.Context, prompt string) (string, error)
	AskMessages(ctx context.Context, messages []llm.Message, attachments []llm.Attachment) (string, error)
}

// CharacterLLM builds a model's character/dialogue prompt in its own format and
// generates the reply. Each model (completion vs chat) implements it differently.
type CharacterLLM interface {
	CharacterReply(ctx context.Context, card *db.Card, requester, message string) (string, error)
	DialogueReply(ctx context.Context, card *db.Card, scenario string, history ...string) (string, error)
}

type TTSClient interface {
	TTS(ctx context.Context, text string, refAudio []byte) ([]byte, []whisperx.Timiing, error)
}
