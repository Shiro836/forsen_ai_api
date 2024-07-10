package llm

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

type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

type Config struct {
	URL         string `yaml:"url"`
	AccessToken string `yaml:"access_token"`

	Model     string `yaml:"model"`
	MaxTokens int    `yaml:"max_tokens"`
}

type Client struct {
	httpClient HTTPClient
	cfg        *Config
}

func New(httpClient HTTPClient, cfg *Config) *Client {
	return &Client{
		httpClient: httpClient,
		cfg:        cfg,
	}
}

func (c *Client) Ask(ctx context.Context, prompt string) (string, error) {
	variants, err := c.reqAi(ctx, &aiReq{
		Model: c.cfg.Model,

		Prompt: prompt,

		MaxTokens: 256,
		Stop:      []string{"###", "<START>", "<END>"},

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
	Model            string   `json:"model"`
	Prompt           string   `json:"prompt"`
	MaxTokens        int      `json:"max_tokens"`
	Temperature      float64  `json:"temperature"`
	FrequencyPenalty float64  `json:"frequency_penalty"`
	Stop             []string `json:"stop"`
}

type choice struct {
	Text string `json:"text"`
}

type aiResp struct {
	Choices []choice `json:"choices"`
}

func (c *Client) reqAi(ctx context.Context, req *aiReq) ([]string, error) {
	data, err := json.Marshal(&req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal ai request struct: %w", err)
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.URL, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("failed to create ai http request: %w", err)
	}

	request.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.cfg.AccessToken))

	start := time.Now()

	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("failed to do ai http request: %w", err)
	}
	defer tools.DrainAndClose(response.Body)

	responseData, err := io.ReadAll(response.Body)
	if err != nil {
		metrics.LLMErrors.WithLabelValues("500").Inc()
		return nil, fmt.Errorf("failed to read ai http response body: %w", err)
	}

	if response.StatusCode != http.StatusOK {
		metrics.LLMErrors.WithLabelValues(strconv.Itoa(response.StatusCode)).Inc()
		return nil, fmt.Errorf("unexpected status code: %d, body: %s", response.StatusCode, string(responseData))
	}

	var resp *aiResp

	if err := json.Unmarshal(responseData, &resp); err != nil {
		metrics.LLMErrors.WithLabelValues("500").Inc()
		return nil, fmt.Errorf("failed to unmarshal ai http response body: %w", err)
	}

	metrics.LLMQueryTime.Observe(time.Since(start).Seconds())

	ans := make([]string, 0, len(resp.Choices))
	for _, choice := range resp.Choices {
		ans = append(ans, choice.Text)
	}

	return ans, nil
}
