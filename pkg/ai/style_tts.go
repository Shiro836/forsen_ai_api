package ai

import (
	"app/pkg/tools"
	"app/pkg/whisperx"

	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	_ "embed"
)

type StyleTTSConfig struct {
	URL string `yaml:"url"`
}

type StyleTTSClient struct {
	cfg        *StyleTTSConfig
	httpClient HTTPClient
}

func NewStyleTTSClient(httpClient HTTPClient, cfg *StyleTTSConfig) *StyleTTSClient {
	return &StyleTTSClient{
		httpClient: httpClient,
		cfg:        cfg,
	}
}

type ttsReq struct {
	Text     string `json:"text"`
	RefAudio []byte `json:"ref_audio"`

	Alpha float64 `json:"alpha"`
	Beta  float64 `json:"beta"`

	EmbeddingScale int `json:"embedding_scale"`
}

type ttsResp struct {
	Audio    []byte             `json:"audio"`
	Error    string             `json:"error"`
	Segments []whisperx.Segment `json:"segments"`
}

var _ TTSEngine = (*StyleTTSClient)(nil)

func (c *StyleTTSClient) TTS(ctx context.Context, msg string, refAudio []byte) ([]byte, []whisperx.Timiing, error) {
	if len(refAudio) == 0 {
		return nil, nil, fmt.Errorf("no audio provided")
	}

	start := time.Now()

	req := &ttsReq{
		Text:     msg,
		RefAudio: refAudio,

		Alpha: 0.5,
		Beta:  0.8,

		EmbeddingScale: 1,
	}

	data, err := json.Marshal(&req)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.URL, bytes.NewReader(data))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create request: %w", err)
	}
	request.Header.Add("Content-Type", "application/json")

	resp, err := c.httpClient.Do(request)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to post to tts server: %w", err)
	}
	defer tools.DrainAndClose(resp.Body)

	respData, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read body: %w", err)
	}

	if resp.StatusCode > 299 {
		metrics.TTSErrors.WithLabelValues(strconv.Itoa(resp.StatusCode)).Inc()
		return nil, nil, fmt.Errorf("status code %d, err - %s", resp.StatusCode, string(respData))
	}

	ttsResp := &ttsResp{}
	err = json.Unmarshal(respData, &ttsResp)
	if err != nil {
		metrics.TTSErrors.WithLabelValues("500").Inc()
		return nil, nil, fmt.Errorf("failed to unmarshal tts resp data: %w", err)
	}

	if len(ttsResp.Error) > 0 {
		return nil, nil, fmt.Errorf("tts api returned: %s", ttsResp.Error)
	}

	metrics.TTSQueryTime.Observe(time.Since(start).Seconds())

	timings := make([]whisperx.Timiing, 0, len(ttsResp.Segments))

	for _, segment := range ttsResp.Segments {
		for _, word := range segment.Words {
			timings = append(timings, whisperx.Timiing{
				Text:  word.Word,
				Start: time.Duration(word.Start * float64(time.Second)),
				End:   time.Duration(word.End * float64(time.Second)),
			})
		}
	}

	return ttsResp.Audio, timings, nil
}
