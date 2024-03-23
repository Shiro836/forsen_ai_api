package ai

import (
	"app/pkg/tools"

	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

type VLLMConfig struct {
	URL string `yaml:"url"`
}

type VLLMClient struct {
	httpClient HTTPClient
	cfg        *VLLMConfig
}

func NewVLLMClient(httpClient HTTPClient, cfg *VLLMConfig) *VLLMClient {
	return &VLLMClient{
		httpClient: httpClient,
		cfg:        cfg,
	}
}

func (c *VLLMClient) Ask(ctx context.Context, prompt string) (string, error) {
	variants, err := c.reqAi(ctx, &aiReq{
		N:         5,
		Prompt:    prompt,
		MaxTokens: 256,
		BestOf:    10,
		TopK:      40,
		TopP:      0.95,
		Stop:      []string{"###", "<START>"},

		Temperature:      0.5,
		FrequencyPenalty: 0.9,
	})
	if err != nil {
		return "", fmt.Errorf("failed to do ai request: %w", err)
	}

	longest := ""

	for _, variant := range variants {
		if len(variant) > len(longest) {
			longest = variant
		}
	}

	return longest, nil
}

type aiReq struct {
	N                int      `json:"n,omitempty"`
	Prompt           string   `json:"prompt"`
	MaxTokens        int      `json:"max_tokens"`
	TopP             float32  `json:"top_p"`
	TopK             int      `json:"top_k"`
	BestOf           int      `json:"best_of"`
	Stop             []string `json:"stop"`
	Temperature      float32  `json:"temperature"`
	FrequencyPenalty float32  `json:"frequency_penalty"`
}

type aiResp struct {
	Responses []string `json:"text"`
}

func (c *VLLMClient) reqAi(ctx context.Context, req *aiReq) ([]string, error) {
	data, err := json.Marshal(&req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal ai request struct: %w", err)
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.URL, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("failed to create ai http request: %w", err)
	}

	start := time.Now()

	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("failed to do ai http request: %w", err)
	}
	defer tools.DrainAndClose(response.Body)

	if response.StatusCode != http.StatusOK {
		metrics.LLMErrors.WithLabelValues(strconv.Itoa(response.StatusCode)).Inc()
		return nil, fmt.Errorf("unexpected status code: %d", response.StatusCode)
	}

	responseData, err := io.ReadAll(response.Body)
	if err != nil {
		metrics.LLMErrors.WithLabelValues("500").Inc()
		return nil, fmt.Errorf("failed to read ai http response body: %w", err)
	}

	var resp *aiResp

	if err := json.Unmarshal(responseData, &resp); err != nil {
		metrics.LLMErrors.WithLabelValues("500").Inc()
		return nil, fmt.Errorf("failed to unmarshal ai http response body: %w", err)
	}

	for i := range resp.Responses {
		resp.Responses[i] = resp.Responses[i][len(req.Prompt):]
	}

	metrics.LLMQueryTime.Observe(time.Since(start).Seconds())

	return resp.Responses, nil
}
