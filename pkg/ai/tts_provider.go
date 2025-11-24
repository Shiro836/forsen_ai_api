package ai

import (
	"context"

	"app/pkg/whisperx"
)

type TTSEngine interface {
	TTS(ctx context.Context, text string, voiceReference []byte) ([]byte, []whisperx.Timiing, error)
}
