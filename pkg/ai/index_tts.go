package ai

import (
	"app/pkg/ffmpeg"
	"app/pkg/tools"
	"app/pkg/whisperx"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// IndexTTSConfig holds basic configuration for the IndexTTS HTTP API.
type IndexTTSConfig struct {
	URL string `yaml:"url"`
}

// IndexTTSClient wraps calls to the IndexTTS v2 HTTP API.
type IndexTTSClient struct {
	httpClient HTTPClient
	cfg        *IndexTTSConfig
}

// NewIndexTTSClient constructs a new IndexTTS client.
func NewIndexTTSClient(httpClient HTTPClient, cfg *IndexTTSConfig) *IndexTTSClient {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}

	return &IndexTTSClient{
		httpClient: httpClient,
		cfg:        cfg,
	}
}

// EmoControlMethod mirrors the control methods supported by IndexTTS.
type EmoControlMethod int

const (
	EmoControlMethodNone EmoControlMethod = iota
	EmoControlMethodAudioReference
	EmoControlMethodVector
	EmoControlMethodText
)

// IndexTTS2Request encapsulates the IndexTTS v2 synthesis payload.
type IndexTTS2Request struct {
	Text                     string           `json:"text"`
	SpeakerAudioPath         string           `json:"spk_audio_path"`
	EmoControlMethod         EmoControlMethod `json:"emo_control_method"`
	EmoRefPath               *string          `json:"emo_ref_path,omitempty"`
	EmoWeight                float64          `json:"emo_weight"`
	EmoVector                []float64        `json:"emo_vec"`
	EmoText                  *string          `json:"emo_text,omitempty"`
	EmoRandom                bool             `json:"emo_random"`
	MaxTextTokensPerSentence int              `json:"max_text_tokens_per_sentence"`
}

// cloneWithDefaults copies the request and applies the defaults expected by the API.
func (r *IndexTTS2Request) cloneWithDefaults() *IndexTTS2Request {
	if r == nil {
		return nil
	}

	out := *r

	out.Text = strings.TrimSpace(out.Text)
	out.SpeakerAudioPath = strings.TrimSpace(out.SpeakerAudioPath)

	if out.EmoWeight == 0 {
		out.EmoWeight = 1
	}

	if out.MaxTextTokensPerSentence == 0 {
		out.MaxTextTokensPerSentence = 120
	}

	if len(out.EmoVector) == 0 {
		out.EmoVector = make([]float64, 8)
	} else if len(out.EmoVector) < 8 {
		vec := make([]float64, 8)
		copy(vec, out.EmoVector)
		out.EmoVector = vec
	} else {
		out.EmoVector = append([]float64(nil), out.EmoVector...)
	}

	return &out
}

type indexTTSServerError struct {
	Status string `json:"status"`
	Error  string `json:"error"`
}

// Synthesize executes a request against the IndexTTS server and returns the WAV bytes.
func (c *IndexTTSClient) Synthesize(ctx context.Context, req *IndexTTS2Request) ([]byte, error) {
	if c == nil || c.cfg == nil || strings.TrimSpace(c.cfg.URL) == "" {
		return nil, fmt.Errorf("index tts client is not configured")
	}

	if req == nil {
		return nil, fmt.Errorf("nil request provided")
	}

	safeReq := req.cloneWithDefaults()

	if safeReq.Text == "" {
		return nil, fmt.Errorf("text must not be empty")
	}

	if safeReq.SpeakerAudioPath == "" {
		return nil, fmt.Errorf("speaker audio path must not be empty")
	}

	body, err := json.Marshal(safeReq)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal index tts request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.URL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create index tts request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")

	start := time.Now()

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed to call index tts server: %w", err)
	}
	defer tools.DrainAndClose(resp.Body)

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read index tts response: %w", err)
	}

	if resp.StatusCode >= 300 {
		metrics.TTSErrors.WithLabelValues(strconv.Itoa(resp.StatusCode)).Inc()

		var apiErr indexTTSServerError
		var msg string

		if len(respBody) != 0 && json.Unmarshal(respBody, &apiErr) == nil && apiErr.Error != "" {
			msg = apiErr.Error
		} else {
			msg = strings.TrimSpace(string(respBody))
		}

		if msg == "" {
			msg = "unknown error"
		}

		return nil, fmt.Errorf("index tts server returned status %d: %s", resp.StatusCode, msg)
	}

	metrics.TTSQueryTime.Observe(time.Since(start).Seconds())

	return respBody, nil
}

// IndexTTSEngine adapts IndexTTSClient to the TTSEngine interface.
type IndexTTSEngine struct {
	client *IndexTTSClient
	tmpDir string
	ffmpeg *ffmpeg.Client
}

const maxVoiceReferenceDuration = 25 * time.Second

// NewIndexTTSEngine builds an engine writing temporary voice prompts to disk before inference.
func NewIndexTTSEngine(client *IndexTTSClient, ffmpegClient *ffmpeg.Client) *IndexTTSEngine {
	tmpDir := os.TempDir()
	if ffmpegClient != nil {
		if dir := ffmpegClient.TmpDir(); dir != "" {
			tmpDir = dir
		}
	}

	return &IndexTTSEngine{
		client: client,
		tmpDir: tmpDir,
		ffmpeg: ffmpegClient,
	}
}

var _ TTSEngine = (*IndexTTSEngine)(nil)

func (e *IndexTTSEngine) TTS(ctx context.Context, text string, voiceReference []byte) ([]byte, []whisperx.Timiing, error) {
	if e == nil || e.client == nil {
		return nil, nil, fmt.Errorf("index tts engine is not configured")
	}

	if len(voiceReference) == 0 {
		return nil, nil, fmt.Errorf("voice reference is required")
	}

	processedReference, err := e.trimVoiceReference(ctx, voiceReference)
	if err != nil {
		return nil, nil, err
	}

	tmpFile, err := os.CreateTemp(e.tmpDir, "indextts-*.wav")
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create temp voice file: %w", err)
	}

	tmpPath := tmpFile.Name()

	defer func() {
		_ = os.Remove(tmpPath)
	}()

	if _, err := tmpFile.Write(processedReference); err != nil {
		tmpFile.Close()
		return nil, nil, fmt.Errorf("failed to write temp voice file: %w", err)
	}

	if err := tmpFile.Close(); err != nil {
		return nil, nil, fmt.Errorf("failed to close temp voice file: %w", err)
	}

	if err := os.Chmod(tmpPath, 0o644); err != nil {
		return nil, nil, fmt.Errorf("failed to set temp voice file permissions: %w", err)
	}

	// emoText := text

	audio, err := e.client.Synthesize(ctx, &IndexTTS2Request{
		Text:             text,
		SpeakerAudioPath: tmpPath,
		EmoControlMethod: EmoControlMethodAudioReference,
		// EmoText:          &emoText,
		EmoRefPath: &tmpPath,
	})
	if err != nil {
		return nil, nil, err
	}

	return audio, nil, nil
}

func (e *IndexTTSEngine) trimVoiceReference(ctx context.Context, voiceReference []byte) ([]byte, error) {
	if e.ffmpeg == nil {
		return voiceReference, nil
	}

	trimmed, err := e.ffmpeg.TrimToWav(ctx, voiceReference, maxVoiceReferenceDuration)
	if err != nil {
		return nil, fmt.Errorf("failed to trim voice reference: %w", err)
	}

	return trimmed, nil
}
