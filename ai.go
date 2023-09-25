package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
)

type AiReq struct {
	N                int      `json:"n,omitempty"`
	Prompt           string   `json:"prompt"`
	MaxTokens        int      `json:"max_tokens"`
	Stop             []string `json:"stop"`
	Temperature      float32  `json:"temperature"`
	FrequencyPenalty float32  `json:"frequency_penalty"`
}

func ReqAI(ctx context.Context, promptContext, msg, forsenReplyStart string) (string, error) {
	prefix := "###CONTEXT: " + promptContext + " ###OTHER: " + msg + " ###FORSEN: " + forsenReplyStart

	req := &AiReq{
		Prompt:           prefix,
		MaxTokens:        1024,
		Stop:             []string{"###", "</s>"},
		Temperature:      0.5,
		FrequencyPenalty: 0.5,
	}

	data, err := json.Marshal(&req)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	resp, err := httpClient.Post(aiUrl, "application/json", bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("failed to post to ai server: %w", err)
	}

	respData, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read body: %w", err)
	}

	if len(respData) < len(prefix) {
		return "", fmt.Errorf("respData is short: %d", len(respData))
	}

	return forsenReplyStart + string(respData[len(prefix)+10:len(respData)-3]), nil
}
