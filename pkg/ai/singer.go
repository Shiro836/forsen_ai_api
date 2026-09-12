package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"app/pkg/tools"
	"app/pkg/whisperx"
)

// SingerConfig points at the singing service. Timeout bounds one /sing call;
// the service runs one job at a time and queues the rest, so a message with
// several sung spans waits on all of them.
type SingerConfig struct {
	URL     string        `yaml:"url"`
	Timeout time.Duration `yaml:"timeout"`
}

type Melody struct {
	ID         string  `json:"id"`
	Number     int     `json:"number"`
	Name       string  `json:"name"`
	TotalNotes int     `json:"total_notes"`
	DurationS  float64 `json:"duration_s"`
}

// MelodyRandom asks the service to pick the melody.
const MelodyRandom = "random"

type SingerClient struct {
	httpClient HTTPClient
	cfg        *SingerConfig
}

func NewSingerClient(httpClient HTTPClient, cfg *SingerConfig) *SingerClient {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: cfg.Timeout}
	}

	return &SingerClient{httpClient: httpClient, cfg: cfg}
}

func (c *SingerClient) Melodies(ctx context.Context) ([]Melody, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(c.cfg.URL, "/")+"/melodies", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create melodies request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to call singer: %w", err)
	}
	defer tools.DrainAndClose(resp.Body)

	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("singer melodies returned status %d", resp.StatusCode)
	}

	var melodies []Melody
	if err := json.NewDecoder(resp.Body).Decode(&melodies); err != nil {
		return nil, fmt.Errorf("failed to decode melodies: %w", err)
	}

	return melodies, nil
}

// MelodyAudio returns the WAV preview of a melody by id, name or number.
func (c *SingerClient) MelodyAudio(ctx context.Context, melody string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(c.cfg.URL, "/")+"/melodies/"+url.PathEscape(melody)+"/audio", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create melody audio request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to call singer: %w", err)
	}
	defer tools.DrainAndClose(resp.Body)

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read melody audio: %w", err)
	}

	if resp.StatusCode >= 300 {
		var apiErr singResponse
		msg := strings.TrimSpace(string(body))
		if json.Unmarshal(body, &apiErr) == nil && apiErr.Error != "" {
			msg = apiErr.Error
		}
		if resp.StatusCode == http.StatusNotFound {
			return nil, fmt.Errorf("%w: %s", ErrUnknownMelody, msg)
		}
		return nil, fmt.Errorf("singer melody audio returned status %d: %s", resp.StatusCode, msg)
	}

	return body, nil
}

// ErrUnknownMelody is returned by ResolveMelody for a spec the service has
// no melody for, as opposed to a failed lookup.
var ErrUnknownMelody = errors.New("unknown melody")

// ResolveMelody maps a tag argument to a melody name the service accepts: a
// number, a name (case-insensitive), or empty/"random" for a random pick.
func (c *SingerClient) ResolveMelody(ctx context.Context, spec string) (string, error) {
	if spec == "" || strings.EqualFold(spec, MelodyRandom) {
		return MelodyRandom, nil
	}

	melodies, err := c.Melodies(ctx)
	if err != nil {
		return "", err
	}

	number, numErr := strconv.Atoi(spec)

	for _, m := range melodies {
		if (numErr == nil && m.Number == number) || strings.EqualFold(m.Name, spec) {
			return m.Name, nil
		}
	}

	return "", fmt.Errorf("%w: %q", ErrUnknownMelody, spec)
}

type singRequest struct {
	Text             string `json:"text"`
	Melody           string `json:"melody"`
	SpeakerAudioPath string `json:"spk_audio_path"`
}

type singWord struct {
	Word  string  `json:"word"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
}

type singResponse struct {
	Audio  []byte     `json:"audio"`
	Words  []singWord `json:"words"`
	Status string     `json:"status"`
	Error  string     `json:"error"`
}

// Sing renders text on melody in the voice at spkAudioPath (a local wav path,
// as for IndexTTS2Request) and returns WAV bytes with word timings.
func (c *SingerClient) Sing(ctx context.Context, text, melody, spkAudioPath string) ([]byte, []whisperx.Timiing, error) {
	body, err := json.Marshal(singRequest{Text: text, Melody: melody, SpeakerAudioPath: spkAudioPath})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to marshal sing request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(c.cfg.URL, "/")+"/sing", bytes.NewReader(body))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create sing request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	start := time.Now()

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to call singer: %w", err)
	}
	defer tools.DrainAndClose(resp.Body)

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read sing response: %w", err)
	}

	var parsed singResponse
	if jsonErr := json.Unmarshal(respBody, &parsed); jsonErr != nil && resp.StatusCode < 300 {
		return nil, nil, fmt.Errorf("failed to decode sing response: %w", jsonErr)
	}

	if resp.StatusCode >= 300 {
		metrics.SingErrors.WithLabelValues(strconv.Itoa(resp.StatusCode)).Inc()

		msg := parsed.Error
		if msg == "" {
			msg = strings.TrimSpace(string(respBody))
		}

		return nil, nil, fmt.Errorf("singer returned status %d: %s", resp.StatusCode, msg)
	}

	metrics.SingQueryTime.Observe(time.Since(start).Seconds())

	if len(parsed.Audio) == 0 {
		return nil, nil, fmt.Errorf("sing response contained no audio")
	}

	timings := make([]whisperx.Timiing, 0, len(parsed.Words))
	for _, w := range parsed.Words {
		timings = append(timings, whisperx.Timiing{
			Text:  w.Word,
			Start: time.Duration(w.Start * float64(time.Second)),
			End:   time.Duration(w.End * float64(time.Second)),
		})
	}

	return parsed.Audio, timings, nil
}

// ReferenceFiler turns voice reference bytes into a local wav path the
// singing service can open.
type ReferenceFiler interface {
	ReferencePath(ctx context.Context, voiceReference []byte) (string, error)
}

type SingerEngine struct {
	client *SingerClient
	refs   ReferenceFiler
}

func NewSingerEngine(client *SingerClient, refs ReferenceFiler) *SingerEngine {
	return &SingerEngine{client: client, refs: refs}
}

func (e *SingerEngine) Sing(ctx context.Context, text, melody string, voiceReference []byte) ([]byte, []whisperx.Timiing, error) {
	if len(voiceReference) == 0 {
		return nil, nil, fmt.Errorf("voice reference is required")
	}

	refPath, err := e.refs.ReferencePath(ctx, voiceReference)
	if err != nil {
		return nil, nil, err
	}

	return e.client.Sing(ctx, text, melody, refPath)
}

func (e *SingerEngine) ResolveMelody(ctx context.Context, spec string) (string, error) {
	return e.client.ResolveMelody(ctx, spec)
}

func (e *SingerEngine) Melodies(ctx context.Context) ([]Melody, error) {
	return e.client.Melodies(ctx)
}
