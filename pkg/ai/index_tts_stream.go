package ai

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// StreamChunk is one finished sentence of a streaming synthesis. Audio is a
// standalone WAV; concatenating all chunks of a stream reproduces the batch
// output exactly (inter-sentence silence is baked into non-final chunks).
// SpeechStart/SpeechEnd bound the speech within the concatenated stream,
// excluding that trailing silence.
type StreamChunk struct {
	Text        string
	SpeechStart time.Duration
	SpeechEnd   time.Duration
	Audio       []byte
}

// StreamingTTSEngine is implemented by engines that can deliver synthesis
// sentence-by-sentence. fn is called once per chunk in stream order; returning
// an error cancels the remaining synthesis server-side.
type StreamingTTSEngine interface {
	TTSStream(ctx context.Context, text string, voiceReference []byte, fn func(StreamChunk) error) error
}

type streamLine struct {
	Text         string  `json:"text"`
	Start        float64 `json:"start"`
	End          float64 `json:"end"`
	SamplingRate int     `json:"sampling_rate"`
	Audio        []byte  `json:"audio"`
	Done         bool    `json:"done"`
	Error        string  `json:"error"`
}

func (c *IndexTTSClient) streamURL() (string, error) {
	if c == nil || c.cfg == nil {
		return "", fmt.Errorf("index tts client is not configured")
	}

	u, err := url.Parse(c.cfg.URL)
	if err != nil {
		return "", fmt.Errorf("failed to parse index tts url: %w", err)
	}

	u.Path = "/tts_stream"

	return u.String(), nil
}

// SynthesizeStream calls /tts_stream and invokes fn per NDJSON sentence line.
// Closing the response body on error/cancel aborts the remaining GPU decodes
// server-side.
func (c *IndexTTSClient) SynthesizeStream(ctx context.Context, req *IndexTTS2Request, fn func(StreamChunk) error) error {
	streamURL, err := c.streamURL()
	if err != nil {
		return err
	}

	safeReq := req.cloneWithDefaults()
	if safeReq == nil || safeReq.Text == "" {
		return fmt.Errorf("text must not be empty")
	}
	if safeReq.SpeakerAudioPath == "" {
		return fmt.Errorf("speaker audio path must not be empty")
	}

	body, err := json.Marshal(safeReq)
	if err != nil {
		return fmt.Errorf("failed to marshal index tts request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, streamURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("failed to create index tts stream request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	start := time.Now()

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("failed to call index tts stream: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		metrics.TTSErrors.WithLabelValues(strconv.Itoa(resp.StatusCode)).Inc()
		return fmt.Errorf("index tts stream returned status %d", resp.StatusCode)
	}

	scanner := bufio.NewScanner(resp.Body)
	// a 60s+ sentence chunk of base64 WAV can exceed the default 64KB line cap
	scanner.Buffer(make([]byte, 0, 1<<20), 64<<20)

	sawTerminator := false

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}

		var parsed streamLine
		if err := json.Unmarshal(line, &parsed); err != nil {
			return fmt.Errorf("failed to unmarshal stream line: %w", err)
		}

		if parsed.Error != "" {
			return fmt.Errorf("index tts stream failed mid-stream: %s", parsed.Error)
		}
		if parsed.Done {
			sawTerminator = true
			break
		}

		chunk := StreamChunk{
			Text:        parsed.Text,
			SpeechStart: time.Duration(parsed.Start * float64(time.Second)),
			SpeechEnd:   time.Duration(parsed.End * float64(time.Second)),
			Audio:       parsed.Audio,
		}

		if err := fn(chunk); err != nil {
			return err
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("index tts stream read error: %w", err)
	}
	if !sawTerminator {
		return fmt.Errorf("index tts stream ended without terminator")
	}

	metrics.TTSQueryTime.Observe(time.Since(start).Seconds())

	return nil
}

var _ StreamingTTSEngine = (*IndexTTSEngine)(nil)

// TTSStream implements StreamingTTSEngine on top of /tts_stream.
func (e *IndexTTSEngine) TTSStream(ctx context.Context, text string, voiceReference []byte, fn func(StreamChunk) error) error {
	if e == nil || e.client == nil {
		return fmt.Errorf("index tts engine is not configured")
	}
	if len(voiceReference) == 0 {
		return fmt.Errorf("voice reference is required")
	}

	text, emotions := ExtractEmotions(text)

	refPath, err := e.referencePath(ctx, voiceReference)
	if err != nil {
		return err
	}

	req := &IndexTTS2Request{
		Text:             text,
		SpeakerAudioPath: refPath,
		EmoControlMethod: EmoControlMethodAudioReference,
		EmoRefPath:       &refPath,
	}

	if vec := EmotionVector(emotions); vec != nil {
		req.EmoControlMethod = EmoControlMethodVector
		req.EmoRefPath = nil
		req.EmoVector = vec
	}

	return e.client.SynthesizeStream(ctx, req, fn)
}

// referencePath writes the processed voice reference to a content-addressed
// file and returns its path. The fork's reference cache is keyed on
// (path, mtime, size), so a stable path per content makes it actually hit;
// files are left in place deliberately — they are the cache.
func (e *IndexTTSEngine) referencePath(ctx context.Context, voiceReference []byte) (string, error) {
	processed, err := e.trimVoiceReference(ctx, voiceReference)
	if err != nil {
		return "", err
	}

	sum := sha256.Sum256(processed)
	path := filepath.Join(e.tmpDir, "indextts-ref-"+hex.EncodeToString(sum[:8])+".wav")

	if _, err := os.Stat(path); err == nil {
		return path, nil
	}

	// write-then-rename so a concurrent request never reads a half-written file
	tmp, err := os.CreateTemp(e.tmpDir, "indextts-ref-*.tmp")
	if err != nil {
		return "", fmt.Errorf("failed to create temp voice file: %w", err)
	}
	tmpPath := tmp.Name()

	if _, err := tmp.Write(processed); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return "", fmt.Errorf("failed to write temp voice file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return "", fmt.Errorf("failed to close temp voice file: %w", err)
	}
	if err := os.Chmod(tmpPath, 0o644); err != nil {
		os.Remove(tmpPath)
		return "", fmt.Errorf("failed to set voice file permissions: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return "", fmt.Errorf("failed to move voice file into place: %w", err)
	}

	return path, nil
}
