package processor

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"app/db"
	"app/internal/app/conns"
	"app/pkg/ai"
	"app/pkg/ffmpeg"
	"app/pkg/whisperx"

	"github.com/google/uuid"
)

// Overlay-v2 wire protocol (see adr/overlay-v2.md). A track is one continuous
// audio timeline (one playTTS-equivalent invocation); an AI redeem plays its
// request and response as two tracks under one msg_id. Audio rides a dedicated
// websocket as self-contained binary frames; control events (track_meta, skip,
// snapshot) stay on the JSON socket.

type trackWord struct {
	W string `json:"w"`
	S int64  `json:"s"` // ms from track start
	E int64  `json:"e"`
}

type chunkHeader struct {
	MsgID    string      `json:"msg_id"`
	TrackID  string      `json:"track_id"`
	Seq      int         `json:"seq"`
	OffsetMs int64       `json:"offset_ms"`
	DurMs    int64       `json:"dur_ms"`
	Text     string      `json:"text"`
	Words    []trackWord `json:"words"`
}

type trackDoneMsg struct {
	MsgID      string `json:"msg_id"`
	TrackID    string `json:"track_id"`
	TotalDurMs int64  `json:"total_dur_ms"`
}

type trackMetaMsg struct {
	MsgID   string `json:"msg_id"`
	TrackID string `json:"track_id"`
	Text    string `json:"text"`
}

// binary frame: [1B type][4B BE header len][header JSON][payload]
func buildFrame(frameType byte, header any, payload []byte) []byte {
	headerJSON, _ := json.Marshal(header)

	frame := make([]byte, 0, 5+len(headerJSON)+len(payload))
	frame = append(frame, frameType)
	frame = binary.BigEndian.AppendUint32(frame, uint32(len(headerJSON)))
	frame = append(frame, headerJSON...)
	frame = append(frame, payload...)

	return frame
}

func chunkFrame(h *chunkHeader, mp3 []byte) []byte {
	return buildFrame(conns.AudioFrameChunk, h, mp3)
}

func trackDoneFrame(msgID, trackID uuid.UUID, total time.Duration) []byte {
	return buildFrame(conns.AudioFrameTrackDone, &trackDoneMsg{
		MsgID:      msgID.String(),
		TrackID:    trackID.String(),
		TotalDurMs: total.Milliseconds(),
	}, nil)
}

// cleanEvent wipes the overlay: caption, karaoke state and prompt images.
func cleanEvent() *conns.DataEvent {
	return &conns.DataEvent{
		EventType: conns.EventTypeClean,
		EventData: []byte("clean"),
	}
}

func trackMetaEvent(msgID, trackID uuid.UUID, text string) *conns.DataEvent {
	data, _ := json.Marshal(&trackMetaMsg{
		MsgID:   msgID.String(),
		TrackID: trackID.String(),
		Text:    text,
	})

	return &conns.DataEvent{
		EventType: conns.EventTypeTrackMeta,
		EventData: data,
	}
}

// wordsFromTimings pairs the display text's whitespace-split words with
// timings. When the aligner delivered (one timing per word) the pairing is
// exact and keeps the original spelling; otherwise word boundaries are
// interpolated proportionally to rune length — self-correcting drift, karaoke
// stays usable without the aligner.
func wordsFromTimings(text string, timings []whisperx.Timiing, total time.Duration) []trackWord {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return nil
	}

	if len(timings) == len(fields) {
		words := make([]trackWord, len(fields))
		for i, f := range fields {
			words[i] = trackWord{W: f, S: timings[i].Start.Milliseconds(), E: timings[i].End.Milliseconds()}
		}
		return words
	}

	return interpolateWords(fields, 0, total)
}

func interpolateWords(fields []string, start, end time.Duration) []trackWord {
	if len(fields) == 0 || end <= start {
		return nil
	}

	totalRunes := 0
	for _, f := range fields {
		totalRunes += len([]rune(f)) + 1
	}

	span := end - start
	words := make([]trackWord, len(fields))
	cum := 0

	for i, f := range fields {
		s := start + time.Duration(int64(span)*int64(cum)/int64(totalRunes))
		cum += len([]rune(f)) + 1
		e := start + time.Duration(int64(span)*int64(cum)/int64(totalRunes))
		words[i] = trackWord{W: f, S: s.Milliseconds(), E: e.Milliseconds()}
	}

	return words
}

// wavDuration reads the duration of a PCM WAV without shelling out to ffprobe
// (streamed chunks are parsed on the play path). Returns false on anything
// that isn't a plain RIFF/PCM file.
func wavDuration(data []byte) (time.Duration, bool) {
	if len(data) < 44 || string(data[0:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		return 0, false
	}

	var sampleRate, byteRate uint32
	pos := 12

	for pos+8 <= len(data) {
		chunkID := string(data[pos : pos+4])
		chunkSize := int(binary.LittleEndian.Uint32(data[pos+4 : pos+8]))

		switch chunkID {
		case "fmt ":
			if pos+24 > len(data) {
				return 0, false
			}
			sampleRate = binary.LittleEndian.Uint32(data[pos+12 : pos+16])
			byteRate = binary.LittleEndian.Uint32(data[pos+16 : pos+20])
		case "data":
			if sampleRate == 0 || byteRate == 0 {
				return 0, false
			}
			return time.Duration(float64(chunkSize) / float64(byteRate) * float64(time.Second)), true
		}

		pos += 8 + chunkSize + chunkSize%2
	}

	return 0, false
}

// alignChunkWords aligns one streamed chunk's text against its audio; on any
// failure it interpolates within the chunk's speech bounds. Never empty for
// non-empty text.
func (s *Service) alignChunkWords(ctx context.Context, logger *slog.Logger, text string, wav []byte, chunkDur, speechStart, speechEnd time.Duration) []trackWord {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return nil
	}

	if s.whisper != nil {
		alignCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		timings, err := s.whisper.Align(alignCtx, text, wav, chunkDur)
		cancel()

		if err == nil && len(timings) == len(fields) {
			words := make([]trackWord, len(fields))
			for i, f := range fields {
				words[i] = trackWord{W: f, S: timings[i].Start.Milliseconds(), E: timings[i].End.Milliseconds()}
			}
			return words
		}
		if err != nil {
			logger.Warn("chunk alignment unavailable, interpolating", "err", err)
		}
	}

	if speechEnd <= speechStart || speechEnd > chunkDur {
		speechStart, speechEnd = 0, chunkDur
	}

	return interpolateWords(fields, speechStart, speechEnd)
}

// playTTSStreaming synthesizes and plays a message through the streaming TTS
// path: sentence chunks are emitted as they leave the GPU, so first audio
// lands seconds after redeem regardless of message length. Falls back to the
// batch path when the engine can't stream or fails before the first chunk.
// The returned channel closes when playback (wall clock) is over, matching
// playTTS semantics.
//
// A non-nil gate defers emission (and the playback clock) until the gate is
// closed, while synthesis and chunk processing run immediately — the way to
// queue a track behind one that is still playing without dead air between
// them. Callers must close the gate, never send on it.
func (s *Service) playTTSStreaming(ctx context.Context, logger *slog.Logger, eventWriter conns.EventWriter, audioWriter conns.AudioWriter, msg string, msgID uuid.UUID, voiceRef []byte, state *ProcessorState, userSettings *db.UserSettings, gate <-chan struct{}) (<-chan struct{}, error) {
	streamer, ok := s.ttsEngine.(ai.StreamingTTSEngine)
	if !ok {
		return s.playTTSBatchFromText(ctx, logger, eventWriter, audioWriter, msg, msgID, voiceRef, state, userSettings, gate)
	}

	ttsLimit := db.DefaultTtsLimitSeconds
	if userSettings.TtsLimit != nil {
		ttsLimit = *userSettings.TtsLimit
	}
	maxDur := time.Duration(ttsLimit) * time.Second

	ttsText := stripForTTS(msg)

	streamCtx, cancelStream := context.WithCancel(ctx)

	chunkCh := make(chan ai.StreamChunk, 8)
	errCh := make(chan error, 1)

	go func() {
		defer close(chunkCh)
		errCh <- streamer.TTSStream(streamCtx, ttsText, voiceRef, func(c ai.StreamChunk) error {
			select {
			case chunkCh <- c:
				return nil
			case <-streamCtx.Done():
				return streamCtx.Err()
			}
		})
	}()

	done := make(chan struct{})

	go func() {
		defer close(done)
		defer cancelStream()

		trackID := uuid.New()

		var playStart time.Time
		var offset time.Duration
		seq := 0
		emitted := false

		type readyChunk struct {
			header *chunkHeader
			mp3    []byte
		}
		var pending []readyChunk

		// nil channel is never selected: nil gate means emit immediately
		gateCh := gate

		emit := func(c readyChunk) {
			if !emitted {
				eventWriter(trackMetaEvent(msgID, trackID, msg))
				playStart = time.Now()
				emitted = true
			}
			// mirror the overlay scheduler: a chunk emitted past its timeline
			// position slips the whole track, so the wall-clock end (which
			// gates "current message" and the next track) must slip with it
			scheduled := playStart.Add(time.Duration(c.header.OffsetMs) * time.Millisecond)
			if now := time.Now(); now.After(scheduled) {
				playStart = playStart.Add(now.Sub(scheduled))
			}
			audioWriter(chunkFrame(c.header, c.mp3))
		}

		// loudness is measured once on the first chunk and reused for the whole
		// track: every chunk gets the same linear gain, matching the batch
		// path's -16 LUFS without flattening dynamics between sentences
		var loudness *ffmpeg.LoudnessStats
		loudnessMeasured := false

		// returns false when the track must stop consuming (error or TTS limit)
		process := func(chunk ai.StreamChunk) bool {
			chunkDur, okDur := wavDuration(chunk.Audio)
			if !okDur {
				var err error
				chunkDur, err = s.getAudioLength(ctx, chunk.Audio)
				if err != nil {
					logger.Error("failed to measure chunk duration, ending track early", "err", err)
					return false
				}
			}

			if !userSettings.DisableAudioNormalization && !loudnessMeasured {
				loudnessMeasured = true
				stats, err := s.ffmpeg.MeasureLoudness(ctx, chunk.Audio)
				if err != nil {
					logger.Warn("loudness measurement failed, encoding without normalization", "err", err)
				} else {
					loudness = stats
				}
			}

			var mp3 []byte
			var err error
			if loudness != nil {
				mp3, err = s.ffmpeg.Ffmpeg2Mp3Normalized(ctx, chunk.Audio, loudness)
			} else {
				mp3, err = s.ffmpeg.Ffmpeg2Mp3(ctx, chunk.Audio, userSettings.DisableAudioNormalization)
			}
			if err != nil {
				logger.Error("failed to encode chunk, ending track early", "err", err)
				return false
			}

			// speech bounds arrive stream-absolute from the engine; make them
			// chunk-local for the interpolation fallback
			words := s.alignChunkWords(ctx, logger, chunk.Text, chunk.Audio, chunkDur, chunk.SpeechStart-offset, chunk.SpeechEnd-offset)
			for i := range words {
				words[i].S += offset.Milliseconds()
				words[i].E += offset.Milliseconds()
			}

			c := readyChunk{
				header: &chunkHeader{
					MsgID:    msgID.String(),
					TrackID:  trackID.String(),
					Seq:      seq,
					OffsetMs: offset.Milliseconds(),
					DurMs:    chunkDur.Milliseconds(),
					Text:     chunk.Text,
					Words:    words,
				},
				mp3: mp3,
			}

			if gateCh == nil {
				emit(c)
			} else {
				pending = append(pending, c)
			}

			seq++
			offset += chunkDur

			// sentence-granular TTS limit: stop consuming, which cancels the
			// remaining GPU decodes server-side
			if offset >= maxDur {
				logger.Info("tts limit reached, closing stream", "offset", offset, "limit", maxDur)
				return false
			}

			return true
		}

		skipTick := time.NewTicker(100 * time.Millisecond)
		defer skipTick.Stop()

		chunksLive := chunkCh
		aborted := false

	receive:
		for chunksLive != nil || gateCh != nil {
			select {
			case <-ctx.Done():
				aborted = true
				break receive
			case <-skipTick.C:
				if state.IsSkipped(msgID) {
					aborted = true
					break receive
				}
			case <-gateCh:
				gateCh = nil
				for _, c := range pending {
					emit(c)
				}
				pending = nil
			case chunk, ok := <-chunksLive:
				if !ok {
					chunksLive = nil
					continue
				}
				if !process(chunk) {
					cancelStream()
					chunksLive = nil
				}
			}
		}

		if aborted {
			cancelStream()
		}

		for range chunkCh {
			// drain whatever the producer had in flight after a cancel
		}

		streamErr := <-errCh

		if !emitted {
			if ctx.Err() != nil || state.IsSkipped(msgID) {
				return
			}
			logger.Warn("stream produced no chunks, falling back to batch TTS", "err", streamErr)
			fallbackDone, err := s.playTTSBatchFromText(ctx, logger, eventWriter, audioWriter, msg, msgID, voiceRef, state, userSettings, gate)
			if err != nil {
				logger.Error("batch fallback failed", "err", err)
				return
			}
			select {
			case <-fallbackDone:
			case <-ctx.Done():
			}
			return
		}

		if streamErr != nil && streamCtx.Err() == nil {
			// mid-stream failure: the track just ends early, like a skip
			logger.Warn("stream failed mid-track", "err", streamErr)
		}

		audioWriter(trackDoneFrame(msgID, trackID, offset))

		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if state.IsSkipped(msgID) {
					eventWriter(skipEvent(msgID, true))
					return
				}
				if time.Since(playStart) > offset {
					return
				}
			}
		}
	}()

	return done, nil
}

// playTTSBatchFromText is the streaming path's fallback: synthesize whole,
// then play through the regular batch pipeline once the gate (if any) opens.
func (s *Service) playTTSBatchFromText(ctx context.Context, logger *slog.Logger, eventWriter conns.EventWriter, audioWriter conns.AudioWriter, msg string, msgID uuid.UUID, voiceRef []byte, state *ProcessorState, userSettings *db.UserSettings, gate <-chan struct{}) (<-chan struct{}, error) {
	audio, timings, err := s.TTSWithTimings(ctx, msg, voiceRef)
	if err != nil {
		return nil, fmt.Errorf("batch synthesis failed: %w", err)
	}

	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			done := make(chan struct{})
			close(done)
			return done, nil
		}
	}

	if state.IsSkipped(msgID) {
		done := make(chan struct{})
		close(done)
		return done, nil
	}

	return s.playTTS(ctx, logger, eventWriter, audioWriter, msg, msgID, audio, timings, state, userSettings)
}
