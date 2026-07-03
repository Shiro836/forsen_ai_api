package audiotree

import (
	"fmt"
	"time"

	"app/pkg/ffmpeg"
)

const (
	slowerTempo = 0.61
	fasterTempo = 1.76
)

type stageKind int

const (
	stageChain stageKind = iota
	stageIRMix
	stageGhost
	stageBGLoop
)

func kindOf(t ffmpeg.FilterType) stageKind {
	switch t {
	case ffmpeg.FilterGhost:
		return stageGhost
	case ffmpeg.FilterRoomEcho, ffmpeg.FilterHallEcho:
		return stageIRMix
	}

	if t.BackgroundAudio() != nil {
		return stageBGLoop
	}

	return stageChain
}

// chainFor returns the -af style chain for plain filters. Sweep and ramp
// filters are parameterized by the duration of the audio they run over.
func chainFor(t ffmpeg.FilterType, dur time.Duration) string {
	secs := dur.Seconds()
	if secs <= 0 {
		secs = 1
	}

	switch t {
	case ffmpeg.FilterOutsideEcho:
		return "aecho=0.8:0.8:0.850000:0.840000"
	case ffmpeg.FilterPitchDown:
		return "rubberband=pitch=0.580000"
	case ffmpeg.FilterPitchUp:
		return "rubberband=pitch=1.760000"
	case ffmpeg.FilterTelephone:
		return "highpass=f=1000,lowpass=f=3000"
	case ffmpeg.FilterMuffled:
		return "lowpass=f=700"
	case ffmpeg.FilterQuiet:
		return "volume=0.300000"
	case ffmpeg.FilterChorus:
		return "chorus=0.7:0.9:40|45|50|60|70|80:0.3|0.25|0.4|0.3|0.4|0.25:0.5|0.6|0.7|0.8|0.9|1:10|11|12|9|8|10"
	case ffmpeg.FilterSlower:
		return fmt.Sprintf("atempo=%f", slowerTempo)
	case ffmpeg.FilterFaster:
		return fmt.Sprintf("atempo=%f", fasterTempo)
	case ffmpeg.FilterRightSide:
		return "pan=stereo|c0=0*c0|c1=1*c0"
	case ffmpeg.FilterLeftSide:
		return "pan=stereo|c0=1*c0|c1=0*c0"
	case ffmpeg.FilterLeftToRight:
		return fmt.Sprintf("apulsator=hz=%.6f:offset_l=0.25:offset_r=0.75", 0.5/secs)
	case ffmpeg.FilterRightToLeft:
		return fmt.Sprintf("apulsator=hz=%.6f:offset_l=0.75:offset_r=0.25", 0.5/secs)
	case ffmpeg.FilterQuietToLoud:
		return fmt.Sprintf("volume='0.1+0.9*t/%.1f':eval=frame", secs)
	case ffmpeg.FilterLoudToQuiet:
		return fmt.Sprintf("volume='1.0-0.9*t/%.1f':eval=frame", secs)
	case ffmpeg.FilterBog:
		return "rubberband=pitch=1.5,chorus=0.7:0.9:55:0.4:0.25:2"
	case ffmpeg.FilterBackgroundCave:
		return "aecho=0.8:0.8:0.97:0.94"
	default:
		return ""
	}
}

// durationMultiplier is how a filter scales the duration of audio passing
// through it, used to compute duration-dependent parameters of later stages
// and to place segment timings on the output timeline.
func durationMultiplier(t ffmpeg.FilterType) float64 {
	switch t {
	case ffmpeg.FilterSlower:
		return 1 / slowerTempo
	case ffmpeg.FilterFaster:
		return 1 / fasterTempo
	default:
		return 1
	}
}
