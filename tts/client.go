package tts

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"app/metrics"
	"app/tools"

	_ "embed"
)

type Config struct {
	URL string `yaml:"url"`
}

var _ HTTPClient = http.DefaultClient

type HTTPClient interface {
	Do(*http.Request) (*http.Response, error)
}

type Client struct {
	cfg        *Config
	httpClient HTTPClient
}

func New(httpClient HTTPClient, cfg *Config) *Client {
	return &Client{
		httpClient: httpClient,
		cfg:        cfg,
	}
}

type ttsReq struct {
	Text     string `json:"text"`
	RefAudio string `json:"ref_audio"`

	Alpha float64 `json:"alpha"`
	Beta  float64 `json:"beta"`

	EmbeddingScale int `json:"embedding_scale"`
}

type ttsResp struct {
	Audio    string    `json:"audio"`
	Timings  []float64 `json:"timings"` // ms
	Words    []string  `json:"words"`
	AudioLen float64   `json:"len"` // ms
}

type Response struct {
	Audio    []byte
	Timings  []float64 // ms
	Words    []string
	AudioLen float64 // ms
}

func (c *Client) TTS(ctx context.Context, msg string, refAudio []byte) (*Response, error) {
	if refAudio == nil {
		return nil, fmt.Errorf("no audio provided")
	}

	start := time.Now()

	req := &ttsReq{
		Text:     msg,
		RefAudio: base64.StdEncoding.EncodeToString(refAudio),

		Alpha: 0.3,
		Beta:  0.7,

		EmbeddingScale: 1,
	}

	data, err := json.Marshal(&req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.URL, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	request.Header.Add("Content-Type", "application/json")

	resp, err := c.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("failed to post to tts server: %w", err)
	}
	defer tools.DrainAndClose(resp.Body)

	respData, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read body: %w", err)
	}

	if resp.StatusCode > 299 {
		metrics.TTSErrors.WithLabelValues(strconv.Itoa(resp.StatusCode)).Inc()
		return nil, fmt.Errorf("status code %d, err - %s", resp.StatusCode, string(respData))
	}

	// fmt.Println(string(respData))

	ttsResp := &ttsResp{}
	err = json.Unmarshal(respData, &ttsResp)
	if err != nil {
		metrics.TTSErrors.WithLabelValues("500").Inc()
		return nil, fmt.Errorf("failed to unmarshal tts resp data: %w", err)
	}

	// fmt.Println(respData)

	bytesData, err := base64.StdEncoding.DecodeString(ttsResp.Audio)
	if err != nil {
		metrics.TTSErrors.WithLabelValues("500").Inc()
		return nil, fmt.Errorf("failed to decode tts response: %w", err)
	}

	metrics.TTSQueryTime.Observe(time.Since(start).Seconds())

	return &Response{
		Audio:    bytesData,
		Timings:  ttsResp.Timings,
		Words:    ttsResp.Words,
		AudioLen: ttsResp.AudioLen,
	}, nil
}
