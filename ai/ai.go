package ai

import (
	"context"
	"fmt"
	"net/http"
)

type Config struct {
	URL string `yaml:"url"`
}

var _ HTTPClient = http.DefaultClient

type HTTPClient interface {
	Do(*http.Request) (*http.Response, error)
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
