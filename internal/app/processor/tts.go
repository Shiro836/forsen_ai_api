package processor

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"app/db"
	"app/internal/app/conns"
	"app/pkg/ai"
	"app/pkg/ffmpeg"
	ttsprocessor "app/pkg/tts_processor"
	"app/pkg/whisperx"

	"github.com/google/uuid"
)

type audioMsg struct {
	Audio []byte `json:"audio"`
	MsgID string `json:"msg_id"`
}

type textMsg struct {
	Text  string `json:"text"`
	MsgID string `json:"msg_id"`
}

func textEvent(text string, msgID uuid.UUID) *conns.DataEvent {
	data, _ := json.Marshal(&textMsg{Text: text, MsgID: msgID.String()})

	return &conns.DataEvent{
		EventType: conns.EventTypeText,
		EventData: data,
	}
}

type skipMsg struct {
	MsgID   string `json:"msg_id"`
	Current bool   `json:"current"`
}

// skipEvent tells the overlay to drop in-flight events for the message;
// current=true additionally wipes the screen.
func skipEvent(msgID uuid.UUID, current bool) *conns.DataEvent {
	data, _ := json.Marshal(&skipMsg{MsgID: msgID.String(), Current: current})

	return &conns.DataEvent{
		EventType: conns.EventTypeSkip,
		EventData: data,
	}
}

// stripForTTS removes asterisks so the voice doesn't read out markdown bold or
// *action* stage directions ("asterisk asterisk"). Words are kept and the
// on-screen text is unaffected (callers pass the original separately to playTTS).
func stripForTTS(msg string) string {
	return strings.ReplaceAll(msg, "*", "")
}

func (s *Service) TTSWithTimings(ctx context.Context, msg string, refAudio []byte) ([]byte, []whisperx.Timiing, error) {
	text := stripForTTS(msg)

	ttsResult, ttsSegments, err := s.ttsEngine.TTS(ctx, text, refAudio)
	if err != nil {
		return nil, nil, err
	}

	// align against what the engine actually spoke: the emotion marker is
	// consumed inside the engine and absent from the audio
	alignText, _ := ai.ExtractEmotions(text)

	if wordTimings, err := s.alignWordTimings(ctx, alignText, ttsResult); err != nil {
		s.logger.Warn("word alignment unavailable, keeping engine timings", "err", err)
	} else {
		ttsSegments = wordTimings
	}

	return ttsResult, ttsSegments, nil
}

// alignWordTimings upgrades the engine's sentence-level timings to word-level
// through the external alignment service. The aligner is optional
// infrastructure: on any failure callers keep the engine timings and the
// overlay degrades to sentence-sized reveals, so this must never fail a
// message — only return an error for the caller to log.
func (s *Service) alignWordTimings(ctx context.Context, text string, wavAudio []byte) ([]whisperx.Timiing, error) {
	if s.whisper == nil {
		return nil, fmt.Errorf("aligner not configured")
	}

	words := strings.Fields(text)
	if len(words) == 0 {
		return nil, fmt.Errorf("no words to align")
	}

	audioLen, err := s.getAudioLength(ctx, wavAudio)
	if err != nil {
		return nil, fmt.Errorf("failed to get audio length: %w", err)
	}

	// alignment sits on the play path; a stalled aligner must not hold back
	// audio when the sentence-timing fallback is one Warn away
	alignCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	timings, err := s.whisper.Align(alignCtx, text, wavAudio, audioLen)
	if err != nil {
		return nil, fmt.Errorf("failed to align: %w", err)
	}

	// the service contract is one timing per whitespace-split word, in order;
	// anything else would desync the prefix mapping in timingTextPrefixes
	if len(timings) != len(words) {
		return nil, fmt.Errorf("aligner returned %d timings for %d words", len(timings), len(words))
	}

	return timings, nil
}

func (s *Service) ChatTTSWithTimings(ctx context.Context, msg string, refAudio []byte) ([]byte, []whisperx.Timiing, error) {
	ttsResult, ttsSegments, err := s.chatTTSEngine.TTS(ctx, stripForTTS(msg), refAudio)
	if err != nil {
		return nil, nil, err
	}

	return ttsResult, ttsSegments, nil
}

func (s *Service) playTTS(ctx context.Context, logger *slog.Logger, eventWriter conns.EventWriter, msg string, msdID uuid.UUID, audio []byte, textTimings []whisperx.Timiing, state *ProcessorState, userSettings *db.UserSettings) (<-chan struct{}, error) {
	mp3Audio, err := s.ffmpeg.Ffmpeg2Mp3(ctx, audio, userSettings.DisableAudioNormalization)
	if err == nil {
		audio = mp3Audio
	} else {
		s.logger.Error("error converting audio to mp3", "err", err)
	}

	audio, err = s.cutTtsAudio(ctx, logger, userSettings, audio)
	if err != nil {
		s.logger.Warn("failed to cut TTS audio", "err", err)
	}

	if !userSettings.DisableAudioNormalization {
		audio, err = s.ffmpeg.NormalizeAudio(ctx, audio)
		if err != nil {
			return nil, fmt.Errorf("failed to normalize TTS audio: %w", err)
		}
	}

	audioLen, err := s.getAudioLength(ctx, audio)
	if err != nil {
		return nil, err
	}

	if len(textTimings) > 0 {
		textTimings[len(textTimings)-1].End = audioLen
	}

	// cumulative original-text prefixes revealed as the corresponding timing
	// starts; the overlay's typewriter appends only the new suffix of each event
	textPrefixes := timingTextPrefixes(msg, textTimings)

	done := make(chan struct{})

	go func() {
		defer close(done)

		if state.IsSkipped(msdID) {
			return
		}

		if len(textPrefixes) == 0 {
			// no timings from the engine: keep showing the full text upfront
			eventWriter(textEvent(msg, msdID))
		}

		audioMsg, err := json.Marshal(&audioMsg{
			Audio: audio,
			MsgID: msdID.String(),
		})
		if err != nil {
			logger.Error("error marshaling audio message", "err", err)
			return
		}

		eventWriter(&conns.DataEvent{
			EventType: conns.EventTypeAudio,
			EventData: audioMsg,
		})

		startTime := time.Now()
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()

		nextSegment := 0

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if state.IsSkipped(msdID) {
					eventWriter(skipEvent(msdID, true))
					return
				}

				elapsed := time.Since(startTime)

				for nextSegment < len(textPrefixes) && elapsed >= textTimings[nextSegment].Start {
					eventWriter(textEvent(textPrefixes[nextSegment], msdID))
					nextSegment++
				}

				if elapsed > audioLen {
					if nextSegment < len(textPrefixes) {
						// make sure the full text is visible at the end
						eventWriter(textEvent(textPrefixes[len(textPrefixes)-1], msdID))
					}
					return
				}
			}
		}
	}()

	return done, nil
}

// timingTextPrefixes maps each timing to a cumulative prefix of the original
// message. Timing texts come back normalized from the TTS (case/punctuation
// differ from msg), so the split points are proportional to the timing texts'
// lengths, snapped forward to a word boundary. The last prefix is always the
// whole message.
func timingTextPrefixes(msg string, timings []whisperx.Timiing) []string {
	if len(timings) == 0 {
		return nil
	}

	msgRunes := []rune(msg)

	total := 0
	for _, t := range timings {
		total += len([]rune(t.Text))
	}

	prefixes := make([]string, len(timings))

	cum := 0
	pos := 0

	for i, t := range timings {
		cum += len([]rune(t.Text))

		if i == len(timings)-1 || total == 0 {
			pos = len(msgRunes)
		} else {
			target := len(msgRunes) * cum / total
			if target < pos {
				target = pos
			}
			for target < len(msgRunes) && msgRunes[target] != ' ' {
				target++
			}
			// include the boundary space: the cached overlay typewriter renders
			// only the first token of a prefix diff synchronously and re-paces
			// the rest at 200ms/word (cancelled by the next event). A diff that
			// starts with a space burns the synchronous slot on it, and with
			// word-level timings the next event lands within 200ms — the actual
			// word would be dropped from the screen permanently.
			for target < len(msgRunes) && msgRunes[target] == ' ' {
				target++
			}
			pos = target
		}

		prefixes[i] = string(msgRunes[:pos])
	}

	prefixes[len(prefixes)-1] = msg

	return prefixes
}

func (s *Service) processUniversalTTSMessage(ctx context.Context, msg string, userSettings *db.UserSettings) ([]ttsprocessor.Action, error) {
	checkVoice := func(voice string) bool {
		name := strings.TrimSpace(voice)
		if len(name) == 0 {
			return false
		}

		_, _, err := s.db.GetVoiceReferenceByShortName(ctx, name)

		return err == nil
	}

	checkFilter := func(filter string) bool {
		if filter == "." {
			return true
		}

		if len(filter) == 0 {
			return false
		}

		if strings.EqualFold(filter, oldTTSFilter) {
			return true
		}

		if ai.IsEmotion(filter) {
			return true
		}

		v, err := strconv.Atoi(filter)
		if err != nil {
			return false
		}

		return v >= 1 && v < int(ffmpeg.FilterLast)
	}

	checkSfx := func(sfx string) bool {
		name := strings.TrimSpace(sfx)
		if len(name) == 0 {
			return false
		}

		if _, err := embeddedSFX.Open("sfx/" + name + ".mp3"); err != nil {
			return false
		}

		return true
	}

	actions, err := ttsprocessor.ProcessMessage(msg, checkVoice, checkFilter, checkSfx)
	if err != nil {
		return nil, err
	}

	maxSfxCount := db.DefaultMaxSfxCount
	if userSettings.MaxSfxCount != nil {
		maxSfxCount = *userSettings.MaxSfxCount
	}

	sfxCount := 0
	var limitedActions []ttsprocessor.Action

	for _, action := range actions {
		if action.Sfx != "" && maxSfxCount != 0 {
			if sfxCount >= maxSfxCount {
				continue
			}
			sfxCount++
		}
		limitedActions = append(limitedActions, action)
	}

	return limitedActions, nil
}

func (s *Service) playUniversalTTS(ctx context.Context, logger *slog.Logger, eventWriter conns.EventWriter, actions []ttsprocessor.Action, msgID uuid.UUID, state *ProcessorState, userSettings *db.UserSettings) (<-chan struct{}, error) {
	combinedAudio, combinedText, combinedTimings, err := s.craftUniversalTTSAudio(ctx, logger, actions, userSettings)
	if err != nil {
		done := make(chan struct{})
		close(done)
		logger.Error("error crafting universal TTS audio", "err", err)
		return done, err
	}

	return s.playTTS(ctx, logger, eventWriter, combinedText, msgID, combinedAudio, combinedTimings, state, userSettings)
}

// oldTTSFilter routes a segment through StyleTTS2 instead of IndexTTS.
const oldTTSFilter = "old"

type actionFilters struct {
	emotions     []string
	audioFilters []string
	oldTTS       bool
}

func parseFilters(filters []string) actionFilters {
	var r actionFilters
	for _, f := range filters {
		switch {
		case strings.EqualFold(f, oldTTSFilter):
			r.oldTTS = true
		case ai.IsEmotion(f):
			r.emotions = append(r.emotions, f)
		default:
			r.audioFilters = append(r.audioFilters, f)
		}
	}

	return r
}

func (s *Service) getVoiceReference(ctx context.Context, logger *slog.Logger, voice string) (uuid.UUID, []byte, error) {
	if voice == "" {
		return uuid.Nil, nil, fmt.Errorf("empty voice")
	}
	logger.Debug("voice reference requested", "voice", voice)

	id, card, err := s.db.GetVoiceReferenceByShortName(ctx, voice)
	if err != nil {
		return uuid.Nil, nil, fmt.Errorf("failed to get voice reference for '%s': %w", voice, err)
	}

	return id, card.VoiceReference, nil
}
