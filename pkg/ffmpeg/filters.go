package ffmpeg

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path"
	"strings"
	"time"

	_ "embed"

	"github.com/google/uuid"
)

// Embedded background audio files
//
//go:embed background/20-keyboard.mp3
var backgroundKeyboard []byte

//go:embed background/21-typewriter.mp3
var backgroundTypewriter []byte

//go:embed background/22-pen.mp3
var backgroundWriting []byte

//go:embed background/23-iphone.mp3
var backgroundIphone []byte

//go:embed background/25-hospital.mp3
var backgroundHospital []byte

//go:embed background/26-windy.mp3
var backgroundWindy []byte

//go:embed background/27-clock.mp3
var backgroundClock []byte

//go:embed background/28-crackle.mp3
var backgroundCrackles []byte

//go:embed background/29-crickets.mp3
var backgroundCrickets []byte

//go:embed background/30-birds.mp3
var backgroundBirds []byte

//go:embed background/31-lava.mp3
var backgroundLava []byte

//go:embed background/ir_large_hall.wav
var churchImpulseResponse []byte

//go:embed background/ir_church_small.wav
var churchSmallImpulseResponse []byte

// FilterType represents the type of audio filter to apply
type FilterType int

const (
	FilterRoomEcho FilterType = iota + 1
	FilterHallEcho
	FilterOutsideEcho

	FilterPitchDown
	FilterPitchUp

	FilterTelephone
	FilterMuffled
	FilterQuiet
	FilterGhost
	FilterChorus

	FilterSlower
	FilterFaster

	FilterRightSide
	FilterLeftSide
	FilterLeftToRight
	FilterRightToLeft
	FilterQuietToLoud
	FilterLoudToQuiet

	FilterBog
	FilterBackgroundKeyboard
	FilterBackgroundTypewriter
	FilterBackgroundWriting
	FilterBackgroundIphone
	FilterBackgroundCave
	FilterBackgroundHospital
	FilterBackgroundWindy
	FilterBackgroundClock
	FilterBackgroundCrackles
	FilterBackgroundCrickets
	FilterBackgroundBirds
	FilterBackgroundLava

	FilterLast
)

func getBackgroundAudio(filterType FilterType) []byte {
	switch filterType {
	case FilterRoomEcho:
		return churchSmallImpulseResponse
	case FilterHallEcho:
		return churchImpulseResponse
	case FilterGhost:
		return churchImpulseResponse
	case FilterBackgroundKeyboard:
		return backgroundKeyboard
	case FilterBackgroundTypewriter:
		return backgroundTypewriter
	case FilterBackgroundWriting:
		return backgroundWriting
	case FilterBackgroundIphone:
		return backgroundIphone
	case FilterBackgroundHospital:
		return backgroundHospital
	case FilterBackgroundWindy:
		return backgroundWindy
	case FilterBackgroundClock:
		return backgroundClock
	case FilterBackgroundCrackles:
		return backgroundCrackles
	case FilterBackgroundCrickets:
		return backgroundCrickets
	case FilterBackgroundBirds:
		return backgroundBirds
	case FilterBackgroundLava:
		return backgroundLava
	default:
		return nil
	}
}

// mixWithBackgroundAudio mixes the input audio with background audio
func (c *Client) mixWithBackgroundAudio(ctx context.Context, audioData []byte, backgroundAudioData []byte, filterType FilterType) ([]byte, error) {
	// Create temporary input file
	inputPath := path.Join(c.cfg.TmpDir, prefix+uuid.NewString())
	err := os.WriteFile(inputPath, audioData, 0644)
	if err != nil {
		return nil, fmt.Errorf("write input file: %w", err)
	}
	defer os.Remove(inputPath)

	// Create temporary background file
	var backgroundPath string

	if filterType == FilterRoomEcho || filterType == FilterHallEcho || filterType == FilterGhost {
		// For room and hall echo, use .wav extension for impulse response
		backgroundPath = path.Join(c.cfg.TmpDir, prefix+uuid.NewString()+".wav")
	} else {
		// For other background audio, use .mp3 extension
		backgroundPath = path.Join(c.cfg.TmpDir, prefix+uuid.NewString()+".mp3")
	}

	err = os.WriteFile(backgroundPath, backgroundAudioData, 0644)
	if err != nil {
		return nil, fmt.Errorf("write background file: %w", err)
	}
	defer os.Remove(backgroundPath)

	// Create temporary output file
	outputPath := path.Join(c.cfg.TmpDir, prefix+uuid.NewString()+".mp3")
	defer os.Remove(outputPath)

	var args []string

	if filterType == FilterRoomEcho || filterType == FilterHallEcho || filterType == FilterGhost {
		var filterComplex string
		switch filterType {
		case FilterGhost:
			filterComplex = `
				[0] adelay=1000|1000 [input];
				[input] areverse [reverse];
				[reverse] [1] afir=dry=10:wet=10 [afirich];
				[afirich] areverse [afirichreverse];
				[0] adelay=1000|1000 [dry];
				[dry] [afirichreverse] amix=inputs=2:weights=5 10 [out];
			`
		default:
			filterComplex = "[0] [1] afir=dry=10:wet=10 [reverb]; [0] [reverb] amix=inputs=2:weights=10 1 [out]"
		}

		args = []string{
			"-i", inputPath,
			"-i", backgroundPath,
			// "-nostats", "-loglevel", "0",
			"-filter_complex", filterComplex,
			"-map", "[out]",
			"-c:a", "mp3",
			"-b:a", "192k",
			"-ar", "44100",
			"-ac", "2",
			"-y",
			outputPath,
		}
	} else {
		args = []string{
			"-i", inputPath,
			"-stream_loop", "-1", "-i", backgroundPath,
			// "-nostats", "-loglevel", "0",
			"-filter_complex", "[0:a][1:a]amix=inputs=2:duration=first:dropout_transition=0[out]",
			"-map", "[out]",
			"-c:a", "mp3",
			"-b:a", "192k",
			"-ar", "44100",
			"-ac", "2",
			"-y",
			outputPath,
		}
	}

	cmd := exec.CommandContext(ctx, "ffmpeg", args...)

	var stderr, stdout bytes.Buffer
	cmd.Stderr = &stderr
	cmd.Stdout = &stdout

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("failed to run ffmpeg background mixing: %w\nffmpeg output:\n%s", err, strings.TrimSpace(stderr.String()+"\n"+stdout.String()))
	}

	output, err := os.ReadFile(outputPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read mixed output file: %w", err)
	}

	return output, nil
}

func (c *Client) applyFilter(ctx context.Context, audioData []byte, filterType FilterType) ([]byte, error) {
	inputPath := path.Join(c.cfg.TmpDir, prefix+uuid.NewString())
	err := os.WriteFile(inputPath, audioData, 0644)
	if err != nil {
		return nil, fmt.Errorf("write input file: %w", err)
	}
	defer os.Remove(inputPath)

	outputPath := path.Join(c.cfg.TmpDir, prefix+uuid.NewString()+".mp3")
	defer os.Remove(outputPath)

	args := []string{
		"-i", inputPath,
		"-nostats", "-loglevel", "0",
	}

	var duration time.Duration
	if filterType == FilterLeftToRight || filterType == FilterRightToLeft ||
		filterType == FilterQuietToLoud || filterType == FilterLoudToQuiet {
		probeResult, err := c.FfprobePath(ctx, inputPath)
		if err != nil {
			return nil, fmt.Errorf("failed to get duration: %w", err)
		}
		duration = probeResult.Duration
	}

	filter := c.buildFilter(filterType, duration)
	if filter != "" {
		args = append(args, "-af", filter)
	}

	args = append(args,
		"-c:a", "mp3",
		"-b:a", "192k",
		"-ar", "44100",
		"-ac", "2",
		"-y",
		outputPath,
	)

	cmd := exec.CommandContext(ctx, "ffmpeg", args...)

	var stderr, stdout bytes.Buffer
	cmd.Stderr = &stderr
	cmd.Stdout = &stdout

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("failed to run ffmpeg filter: %w\nffmpeg output:\n%s", err, strings.TrimSpace(stderr.String()+"\n"+stdout.String()))
	}

	output, err := os.ReadFile(outputPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read filtered output file: %w", err)
	}

	return output, nil
}

// buildFilter constructs the ffmpeg filter string based on the filter type
func (c *Client) buildFilter(filterType FilterType, duration time.Duration) string {
	switch filterType {

	case FilterOutsideEcho:
		outsideDelay := 0.85
		outsideDecay := 0.84
		return fmt.Sprintf("aecho=0.8:0.8:%f:%f", outsideDelay, outsideDecay)

	case FilterPitchDown:
		pitchDownValue := 0.58
		return fmt.Sprintf("rubberband=pitch=%f", pitchDownValue)

	case FilterPitchUp:
		pitchUpValue := 1.76
		return fmt.Sprintf("rubberband=pitch=%f", pitchUpValue)

	case FilterTelephone:
		phoneLowFreq := 1000
		phoneHighFreq := 3000
		return fmt.Sprintf("highpass=f=%d,lowpass=f=%d", phoneLowFreq, phoneHighFreq)

	case FilterMuffled:
		muffledCutoff := 700
		return fmt.Sprintf("lowpass=f=%d", muffledCutoff)

	case FilterQuiet:
		// Quiet: reduce volume
		quietVolume := 0.3
		return fmt.Sprintf("volume=%f", quietVolume)

	case FilterChorus:
		return "chorus=0.7:0.9:40|45|50|60|70|80:0.3|0.25|0.4|0.3|0.4|0.25:0.5|0.6|0.7|0.8|0.9|1:10|11|12|9|8|10"

	case FilterSlower:
		slowerSpeed := 0.61
		return fmt.Sprintf("atempo=%f", slowerSpeed)

	case FilterFaster:
		fasterSpeed := 1.76
		return fmt.Sprintf("atempo=%f", fasterSpeed)

	case FilterRightSide:
		return "pan=stereo|c0=0*c0|c1=1*c0"

	case FilterLeftSide:
		return "pan=stereo|c0=1*c0|c1=0*c0"

	case FilterLeftToRight:
		frequency := 0.5 / duration.Seconds()
		return fmt.Sprintf("apulsator=hz=%.6f:offset_l=0.25:offset_r=0.75", frequency)

	case FilterRightToLeft:
		frequency := 0.5 / duration.Seconds()
		return fmt.Sprintf("apulsator=hz=%.6f:offset_l=0.75:offset_r=0.25", frequency)

	case FilterQuietToLoud:
		return fmt.Sprintf("volume='0.1+0.9*t/%.1f':eval=frame", duration.Seconds())

	case FilterLoudToQuiet:
		return fmt.Sprintf("volume='1.0-0.9*t/%.1f':eval=frame", duration.Seconds())

	case FilterBog:
		return "rubberband=pitch=1.5,chorus=0.7:0.9:55:0.4:0.25:2"

	case FilterBackgroundCave:
		return "aecho=0.8:0.8:0.97:0.94"

	default:
		return "" // No filter applied
	}
}

func (c *Client) ApplyStringFilters(ctx context.Context, audioData []byte, filterNames []string) ([]byte, error) {
	if len(filterNames) == 0 {
		return audioData, nil
	}

	var types []FilterType
	for _, name := range filterNames {
		var filterNum int
		if _, err := fmt.Sscanf(name, "%d", &filterNum); err != nil {
			return nil, fmt.Errorf("invalid filter number: %s", name)
		}

		filterType := FilterType(filterNum)

		if filterType < 1 || filterType >= FilterLast {
			return nil, fmt.Errorf("filter number out of range: %d", filterNum)
		}

		types = append(types, filterType)
	}

	for i, t := range types {
		var err error

		if bgAudio := getBackgroundAudio(t); bgAudio != nil {
			audioData, err = c.mixWithBackgroundAudio(ctx, audioData, bgAudio, t)
		} else {
			audioData, err = c.applyFilter(ctx, audioData, t)
		}

		if err != nil {
			return nil, fmt.Errorf("failed to apply filter %d: %w", i+1, err)
		}
	}

	return audioData, nil
}
