package processor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"time"

	"app/pkg/archive"
	"app/pkg/s3client"

	"github.com/google/uuid"
)

// archiveTrackAudio uploads the MP3 bytes a track streamed to the overlay and,
// on success, points the recorded track at the object. The upload outlives the
// handler's context: a restart mid-message must not lose audio that played.
func (s *Service) archiveTrackAudio(ctx context.Context, logger *slog.Logger, col *archive.Collector, idx int, trackID uuid.UUID, mp3 []byte) {
	if s.s3 == nil || col == nil || idx < 0 || len(mp3) == 0 {
		return
	}
	key := col.AudioKey(trackID)
	col.Go(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		if err := s.s3.PutObject(ctx, s3client.TTSArchiveBucket, key, bytes.NewReader(mp3), int64(len(mp3)), "audio/mpeg"); err != nil {
			logger.Error("failed to archive track audio", "key", key, "err", err)
			return
		}
		col.SetAudioKey(idx, key)
	})
}

func voiceSHA(ref []byte) string {
	if len(ref) == 0 {
		return ""
	}
	sum := sha256.Sum256(ref)
	return hex.EncodeToString(sum[:])
}
