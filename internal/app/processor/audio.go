package processor

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"app/db"
)

// cutTtsAudio cuts audio to the user's TTS limit if it exceeds the limit
func (s *Service) cutTtsAudio(ctx context.Context, logger *slog.Logger, userSettings *db.UserSettings, audio []byte) ([]byte, error) {
	ttsLimit := db.DefaultTtsLimitSeconds // Default to 80 seconds
	if userSettings.TtsLimit != nil {
		ttsLimit = *userSettings.TtsLimit
	}

	audioLen, err := s.getAudioLength(ctx, audio)
	if err != nil {
		s.logger.Warn("failed to get audio length for TTS cutting", "err", err)
		return audio, nil
	}

	maxDuration := time.Duration(ttsLimit) * time.Second
	if audioLen <= maxDuration {
		return audio, nil
	}

	logger.Debug("cutting TTS audio to fit limit", "original_duration", audioLen, "max_duration", maxDuration)

	cutAudio, err := s.ffmpeg.CutAudio(ctx, audio, maxDuration)
	if err != nil {
		logger.Warn("failed to cut audio, using original", "err", err)
		return cutAudio, nil
	}

	return cutAudio, nil
}

func (s *Service) getAudioLength(ctx context.Context, data []byte) (time.Duration, error) {
	res, err := s.ffmpeg.Ffprobe(ctx, data)
	if err != nil {
		return 0, fmt.Errorf("error getting audio length: %w", err)
	}

	return res.Duration, nil
}

func (s *Service) limitFilters(filters []string) []string {
	const (
		maxPerFilter = 3
		maxTotal     = 15
	)

	if len(filters) == 0 {
		return filters
	}

	filterCounts := make(map[string]int)
	var limitedFilters []string

	// Only allow a single spatial/panning filter among 13..16
	spatialUsed := false
	spatialSet := map[string]struct{}{
		"13": {}, // right_side
		"14": {}, // left_side
		"15": {}, // left_to_right
		"16": {}, // right_to_left
	}
	isSpatial := func(filter string) bool {
		_, ok := spatialSet[filter]
		return ok
	}

	for _, filter := range filters {
		if isSpatial(filter) && spatialUsed {
			continue
		}

		if filterCounts[filter] >= maxPerFilter {
			continue
		}

		filterCounts[filter]++
		if isSpatial(filter) {
			spatialUsed = true
		}

		limitedFilters = append(limitedFilters, filter)

		if len(limitedFilters) >= maxTotal {
			break
		}
	}

	return limitedFilters
}

func (s *Service) applyAudioEffects(ctx context.Context, audio []byte, filters ...string) ([]byte, error) {
	if len(filters) == 0 {
		return audio, nil
	}

	limitedFilters := s.limitFilters(filters)

	processedAudio, err := s.ffmpeg.ApplyStringFilters(ctx, audio, limitedFilters)
	if err != nil {
		return nil, fmt.Errorf("failed to apply audio effects: %w", err)
	}

	return processedAudio, nil
}

func (s *Service) getSFX(sfxName string) ([]byte, error) {
	name := strings.TrimSpace(sfxName)
	if len(name) == 0 {
		return nil, fmt.Errorf("empty sfx name")
	}

	data, err := embeddedSFX.ReadFile("sfx/" + name + ".mp3")
	if err != nil {
		return nil, fmt.Errorf("sfx '%s' not found: %w", sfxName, err)
	}

	return data, nil
}
