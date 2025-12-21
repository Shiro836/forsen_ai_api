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
	"app/pkg/ffmpeg"
	ttsprocessor "app/pkg/tts_processor"
	"app/pkg/whisperx"

	"github.com/google/uuid"
)

type audioMsg struct {
	Audio []byte `json:"audio"`
	MsgID string `json:"msg_id"`
}

func (s *Service) TTSWithTimings(ctx context.Context, msg string, refAudio []byte) ([]byte, []whisperx.Timiing, error) {
	ttsResult, ttsSegments, err := s.ttsEngine.TTS(ctx, msg, refAudio)
	if err != nil {
		return nil, nil, err
	}

	return ttsResult, ttsSegments, nil
}

func (s *Service) playTTS(ctx context.Context, logger *slog.Logger, eventWriter conns.EventWriter, msg string, msdID uuid.UUID, audio []byte, textTimings []whisperx.Timiing, state *ProcessorState, userSettings *db.UserSettings) (<-chan struct{}, error) {
	mp3Audio, err := s.ffmpeg.Ffmpeg2Mp3(ctx, audio)
	if err == nil {
		audio = mp3Audio
	} else {
		s.logger.Error("error converting audio to mp3", "err", err)
	}

	audio, err = s.cutTtsAudio(ctx, logger, userSettings, audio)
	if err != nil {
		s.logger.Warn("failed to cut TTS audio", "err", err)
	}

	audioLen, err := s.getAudioLength(ctx, audio)
	if err != nil {
		return nil, err
	}

	if len(textTimings) > 0 {
		textTimings[len(textTimings)-1].End = audioLen
	}

	done := make(chan struct{})

	go func() {
		defer close(done)

		if state.IsSkipped(msdID) {
			return
		}

		eventWriter(&conns.DataEvent{
			EventType: conns.EventTypeText,
			EventData: []byte(msg),
		})

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

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if state.IsSkipped(msdID) {
					eventWriter(&conns.DataEvent{
						EventType: conns.EventTypeSkip,
						EventData: []byte(msdID.String()),
					})
					return
				}

				elapsed := time.Since(startTime)
				if elapsed > audioLen {
					return
				}
			}
		}
	}()

	return done, nil
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

func (s *Service) craftUniversalTTSAudio(ctx context.Context, logger *slog.Logger, actions []ttsprocessor.Action, userSettings *db.UserSettings) ([]byte, string, []whisperx.Timiing, error) {
	var combinedAudio [][]byte
	var combinedText strings.Builder
	var combinedTimings []whisperx.Timiing
	currentOffset := time.Duration(0)
	sfxAccumulated := time.Duration(0)

	concatPadding := 500 * time.Millisecond
	defaultVoice := "obiwan"

	ttsLimit := db.DefaultTtsLimitSeconds // Default to 80 seconds
	if userSettings.TtsLimit != nil {
		ttsLimit = *userSettings.TtsLimit
	}
	maxDuration := time.Duration(ttsLimit) * time.Second

	// Total SFX duration limit (0 = unlimited)
	sfxTotalLimit := db.DefaultSfxTotalLimit
	if userSettings.SfxTotalLimit != nil {
		sfxTotalLimit = *userSettings.SfxTotalLimit
	}
	maxSfxDuration := time.Duration(sfxTotalLimit) * time.Second

actions_loop:
	for _, action := range actions {
		if action.Text != "" && action.Text != " " {
			voice := action.Voice
			if voice == "" {
				voice = defaultVoice
			}

			_, voiceRef, err := s.getVoiceReference(ctx, logger, voice)
			if err != nil {
				logger.Error("error getting voice reference", "err", err, "voice", action.Voice)
				voiceRef = []byte{}
			}

			audio, timings, err := s.TTSWithTimings(ctx, action.Text, voiceRef)
			if err != nil {
				logger.Error("error generating TTS for universal action", "err", err, "text", action.Text)
				continue
			}

			originalAudioLen, err := s.getAudioLength(ctx, audio)
			if err != nil {
				logger.Error("error getting audio length", "err", err)
				continue
			}

			processedAudio, err := s.applyAudioEffects(ctx, audio, action.Filters...)
			if err != nil {
				logger.Error("error applying audio effects", "err", err, "filters", action.Filters)

				processedAudio = audio
			}

			processedAudioLen, err := s.getAudioLength(ctx, processedAudio)
			if err != nil {
				logger.Error("error getting audio length", "err", err)
				continue
			}

			if originalAudioLen != 0 {
				change := float64(processedAudioLen) / float64(originalAudioLen)

				if change > 1.05 || change < 0.95 {
					for i := range timings {
						timings[i].Start = time.Duration(float64(timings[i].Start) * change)
						timings[i].End = time.Duration(float64(timings[i].End) * change)
					}
				}
			}

			combinedAudio = append(combinedAudio, processedAudio)

			if combinedText.Len() > 0 {
				combinedText.WriteString(" ")
			}
			combinedText.WriteString(action.Text)

			for i := range timings {
				timings[i].Start += currentOffset
				timings[i].End += currentOffset
			}
			combinedTimings = append(combinedTimings, timings...)

			audioLen, err := s.getAudioLength(ctx, processedAudio)
			if err != nil {
				logger.Error("error getting audio length", "err", err)
				continue
			}

			currentOffset += audioLen
			currentOffset += concatPadding

			if currentOffset > maxDuration {
				logger.Info("stopping universal TTS generation due to length limit", "current_duration", currentOffset, "max_duration", maxDuration)
				break actions_loop
			}
		}

		if action.Sfx != "" {
			sfxAudio, err := s.getSFX(action.Sfx)
			if err != nil {
				logger.Error("error generating SFX", "err", err, "sfx", action.Sfx)

				if combinedText.Len() > 0 {
					combinedText.WriteString(" ")
				}
				combinedText.WriteString(fmt.Sprintf("[%s]", action.Sfx))

				continue
			}

			processedSFX, err := s.applyAudioEffects(ctx, sfxAudio, action.Filters...)
			if err != nil {
				logger.Error("error applying audio effects to SFX", "err", err, "filters", action.Filters)

				processedSFX = sfxAudio
			}

			sfxLen, err := s.getAudioLength(ctx, processedSFX)
			if err != nil {
				logger.Error("error getting SFX length", "err", err)

				continue
			}

			// Enforce total SFX duration limit (if > 0). If the SFX would exceed the limit, cut it to fit.
			if maxSfxDuration > 0 && sfxAccumulated+sfxLen > maxSfxDuration {
				remaining := maxSfxDuration - sfxAccumulated
				if remaining <= 0 {
					logger.Info("skipping SFX due to total SFX length limit (no remaining budget)", "sfx", action.Sfx, "accumulated", sfxAccumulated, "max_sfx_duration", maxSfxDuration)
					continue
				}

				cutSFX, err := s.ffmpeg.CutAudio(ctx, processedSFX, remaining)
				if err != nil {
					logger.Warn("failed to cut SFX to fit total limit; skipping SFX", "err", err)
					continue
				}

				processedSFX = cutSFX
				sfxLen = remaining
			}

			combinedAudio = append(combinedAudio, processedSFX)

			if combinedText.Len() > 0 {
				combinedText.WriteString(" ")
			}
			combinedText.WriteString(fmt.Sprintf("[%s]", action.Sfx))

			sfxAccumulated += sfxLen
			currentOffset += sfxLen

			if currentOffset > maxDuration {
				logger.Info("stopping universal TTS generation due to length limit", "current_duration", currentOffset, "max_duration", maxDuration)
				break actions_loop
			}
		}
	}

	for i := range combinedTimings {
		combinedTimings[i].Start = min(combinedTimings[i].Start, maxDuration)
		combinedTimings[i].End = min(combinedTimings[i].End, maxDuration)
	}

	if len(combinedAudio) == 0 {
		return nil, "", nil, fmt.Errorf("no audio generated from actions")
	}

	finalAudio, err := s.ffmpeg.ConcatenateAudio(ctx, concatPadding, combinedAudio...)
	if err != nil {
		return nil, "", nil, fmt.Errorf("error concatenating audio: %w", err)
	}

	return finalAudio, combinedText.String(), combinedTimings, nil
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
