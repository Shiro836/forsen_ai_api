package ai

import (
	"app/tools"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

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

func (c *Client) reqAi(ctx context.Context, req *aiReq) ([]string, error) {
	data, err := json.Marshal(&req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal ai request struct: %w", err)
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.URL, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("failed to create ai http request: %w", err)
	}

	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("failed to do ai http request: %w", err)
	}
	defer tools.DrainAndClose(response.Body)

	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code: %d", response.StatusCode)
	}

	responseData, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read ai http response body: %w", err)
	}

	var resp *aiResp

	if err := json.Unmarshal(responseData, &resp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal ai http response body: %w", err)
	}

	for i := range resp.Responses {
		resp.Responses[i] = resp.Responses[i][len(req.Prompt):]
	}

	return resp.Responses, nil
}
