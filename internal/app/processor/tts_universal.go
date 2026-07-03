package processor

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"app/db"
	"app/pkg/ai"
	"app/pkg/audiotree"
	ttsprocessor "app/pkg/tts_processor"
	"app/pkg/whisperx"
)

// useTreeRenderer selects the span-tree renderer for universal TTS audio;
// flip to false for the legacy per-segment renderer.
const useTreeRenderer = true

type universalAudioRenderer interface {
	Render(ctx context.Context, segments []audiotree.Segment, padding time.Duration, disableLimiter bool) ([]byte, []audiotree.Placement, error)
}

func (s *Service) universalRenderer() universalAudioRenderer {
	if useTreeRenderer {
		return audiotree.New(s.ffmpeg)
	}
	return audiotree.NewLegacy(s.ffmpeg)
}

type universalJob struct {
	displayText string
	filters     []string

	isSfx   bool
	sfxName string

	voice   string
	ttsText string
	oldTTS  bool

	audio   []byte
	timings []whisperx.Timiing
	dur     time.Duration
	ok      bool
}

func (s *Service) craftUniversalTTSAudio(ctx context.Context, logger *slog.Logger, actions []ttsprocessor.Action, userSettings *db.UserSettings) ([]byte, string, []whisperx.Timiing, error) {
	concatPadding := 500 * time.Millisecond
	defaultVoice := "obiwan"

	ttsLimit := db.DefaultTtsLimitSeconds
	if userSettings.TtsLimit != nil {
		ttsLimit = *userSettings.TtsLimit
	}
	maxDuration := time.Duration(ttsLimit) * time.Second

	sfxTotalLimit := db.DefaultSfxTotalLimit
	if userSettings.SfxTotalLimit != nil {
		sfxTotalLimit = *userSettings.SfxTotalLimit
	}
	maxSfxDuration := time.Duration(sfxTotalLimit) * time.Second

	var jobs []*universalJob

	for _, action := range actions {
		filters := parseFilters(action.Filters)
		audioFilters := s.limitFilters(filters.audioFilters)

		if action.Text != "" && action.Text != " " {
			voice := action.Voice
			if voice == "" {
				voice = defaultVoice
			}

			jobs = append(jobs, &universalJob{
				displayText: action.Text,
				filters:     audioFilters,
				voice:       voice,
				ttsText:     ai.InsertEmotions(action.Text, filters.emotions),
				oldTTS:      filters.oldTTS,
			})
		}

		if action.Sfx != "" {
			jobs = append(jobs, &universalJob{
				displayText: fmt.Sprintf("[%s]", action.Sfx),
				filters:     audioFilters,
				isSfx:       true,
				sfxName:     action.Sfx,
			})
		}
	}

	var wg sync.WaitGroup
	for _, job := range jobs {
		wg.Go(func() {
			s.generateUniversalJob(ctx, logger, job)
		})
	}
	wg.Wait()

	var segments []audiotree.Segment
	var segmentJobs []*universalJob
	var combinedText strings.Builder

	appendText := func(text string) {
		if combinedText.Len() > 0 {
			combinedText.WriteString(" ")
		}
		combinedText.WriteString(text)
	}

	offset := time.Duration(0)
	sfxAccumulated := time.Duration(0)

	for _, job := range jobs {
		if !job.ok {
			if job.isSfx {
				appendText(job.displayText)
			}
			continue
		}

		audio, dur := job.audio, job.dur

		if job.isSfx && maxSfxDuration > 0 && sfxAccumulated+dur > maxSfxDuration {
			remaining := maxSfxDuration - sfxAccumulated
			if remaining <= 0 {
				logger.Info("skipping SFX due to total SFX length limit (no remaining budget)",
					"sfx", job.sfxName, "accumulated", sfxAccumulated, "max_sfx_duration", maxSfxDuration)
				continue
			}

			cut, err := s.ffmpeg.CutAudio(ctx, audio, remaining)
			if err != nil {
				logger.Warn("failed to cut SFX to fit total limit; skipping SFX", "err", err)
				continue
			}
			audio, dur = cut, remaining
		}
		if job.isSfx {
			sfxAccumulated += dur
		}

		if len(segments) > 0 {
			offset += concatPadding
		}
		offset += dur

		segments = append(segments, audiotree.Segment{
			Audio:    audio,
			Filters:  job.filters,
			Duration: dur,
		})
		segmentJobs = append(segmentJobs, job)
		appendText(job.displayText)

		if offset > maxDuration {
			logger.Info("stopping universal TTS generation due to length limit",
				"current_duration", offset, "max_duration", maxDuration)
			break
		}
	}

	if len(segments) == 0 {
		return nil, "", nil, fmt.Errorf("no audio generated from actions")
	}

	finalAudio, placements, err := s.universalRenderer().Render(ctx, segments, concatPadding, userSettings.DisableAudioNormalization)
	if err != nil {
		return nil, "", nil, fmt.Errorf("error rendering universal TTS audio: %w", err)
	}

	var combinedTimings []whisperx.Timiing
	for i, job := range segmentJobs {
		if job.isSfx || len(job.timings) == 0 || job.dur <= 0 {
			continue
		}

		placement := placements[i]
		scale := float64(placement.End-placement.Start) / float64(job.dur)

		for _, timing := range job.timings {
			combinedTimings = append(combinedTimings, whisperx.Timiing{
				Text:  timing.Text,
				Start: placement.Start + time.Duration(float64(timing.Start)*scale),
				End:   placement.Start + time.Duration(float64(timing.End)*scale),
			})
		}
	}

	for i := range combinedTimings {
		combinedTimings[i].Start = min(combinedTimings[i].Start, maxDuration)
		combinedTimings[i].End = min(combinedTimings[i].End, maxDuration)
	}

	return finalAudio, combinedText.String(), combinedTimings, nil
}

func (s *Service) generateUniversalJob(ctx context.Context, logger *slog.Logger, job *universalJob) {
	if job.isSfx {
		audio, err := s.getSFX(job.sfxName)
		if err != nil {
			logger.Error("error loading SFX", "err", err, "sfx", job.sfxName)
			return
		}
		job.audio = audio
	} else {
		_, voiceRef, err := s.getVoiceReference(ctx, logger, job.voice)
		if err != nil {
			logger.Error("error getting voice reference", "err", err, "voice", job.voice)
			voiceRef = []byte{}
		}

		var audio []byte
		var timings []whisperx.Timiing
		if job.oldTTS {
			audio, timings, err = s.ChatTTSWithTimings(ctx, job.ttsText, voiceRef)
		} else {
			audio, timings, err = s.TTSWithTimings(ctx, job.ttsText, voiceRef)
		}
		if err != nil {
			logger.Error("error generating TTS for universal action", "err", err, "text", job.displayText)
			return
		}

		job.audio = audio
		job.timings = timings
	}

	probe, err := s.ffmpeg.Ffprobe(ctx, job.audio)
	if err != nil {
		logger.Error("error getting audio length", "err", err)
		return
	}

	job.dur = probe.Duration
	job.ok = true
}
