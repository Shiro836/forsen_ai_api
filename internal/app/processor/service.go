package processor

import (
	"log/slog"

	"app/db"
	"app/internal/app/conns"
	"app/pkg/ai"
	"app/pkg/ffmpeg"
	"app/pkg/llm"
	"app/pkg/llmfilter"
	"app/pkg/s3client"
	"app/pkg/whisperx"
)

type Service struct {
	logger        *slog.Logger
	db            *db.DB
	s3            *s3client.Client
	ffmpeg        *ffmpeg.Client
	ttsEngine     ai.TTSEngine
	chatTTSEngine ai.TTSEngine
	whisper       *whisperx.Client
	llmModelRaw   *llm.Client
	imageLlmRaw   *llm.Client
	llmFilter     *llmfilter.Filter
	connManager   *conns.Manager
}

func NewService(logger *slog.Logger, db *db.DB, s3 *s3client.Client, ffmpeg *ffmpeg.Client, ttsEngine ai.TTSEngine, chatTTSEngine ai.TTSEngine, whisper *whisperx.Client, llmModel *llm.Client, imageLlm *llm.Client, llmFilter *llmfilter.Filter, connManager *conns.Manager) *Service {
	return &Service{
		logger:        logger,
		db:            db,
		s3:            s3,
		ffmpeg:        ffmpeg,
		ttsEngine:     ttsEngine,
		chatTTSEngine: chatTTSEngine,
		whisper:       whisper,
		llmModelRaw:   llmModel,
		imageLlmRaw:   imageLlm,
		llmFilter:     llmFilter,
		connManager:   connManager,
	}
}
