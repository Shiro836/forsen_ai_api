package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"app/tools"
)

type AiReq struct {
	N                int      `json:"n,omitempty"`
	Prompt           string   `json:"prompt"`
	MaxTokens        int      `json:"max_tokens"`
	Stop             []string `json:"stop"`
	Temperature      float32  `json:"temperature"`
	FrequencyPenalty float32  `json:"frequency_penalty"`
}

func ReqAI(ctx context.Context, promptContext, memory, msg, forsenReplyStart string) (string, error) {
	prefix := "###CONTEXT: " + promptContext + " " + memory + " ###PROMPT: " + msg + " ###FORSEN: " + forsenReplyStart

	req := &AiReq{
		Prompt:           prefix,
		MaxTokens:        1800,
		Stop:             []string{"###", "</s>"},
		Temperature:      0.7,
		FrequencyPenalty: 0.7,
	}

	data, err := json.Marshal(&req)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, aiUrl, bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}
	request.Header.Add("Content-Type", "application/json")

	resp, err := httpClient.Do(request)
	if err != nil {
		return "", fmt.Errorf("failed to post to ai server: %w", err)
	}
	defer tools.DrainAndClose(resp.Body)

	respData, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read body: %w", err)
	}

	if len(respData) < len(prefix) {
		return "", fmt.Errorf("respData is short: %d", len(respData))
	}

	return forsenReplyStart + string(respData[len(prefix)+10:len(respData)-3]), nil
}
