package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"app/tools"
)

type TTSReq struct {
	Text  string `json:"text"`
	Voice string `json:"voice"`
}

type TTSResp struct {
	Audio string `json:"audio"`
}

func reqTTS(ctx context.Context, msg string, voice string) ([]byte, error) {
	req := &TTSReq{
		Text:  msg,
		Voice: voice,
	}

	data, err := json.Marshal(&req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, ttsUrl, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	request.Header.Add("Content-Type", "application/json")

	resp, err := httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("failed to post to tts server: %w", err)
	}
	defer tools.DrainAndClose(resp.Body)

	respData, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read body: %w", err)
	}

	// fmt.Println(string(respData))

	ttsResp := &TTSResp{}
	err = json.Unmarshal(respData, &ttsResp)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal tts resp data: %w", err)
	}

	// fmt.Println(respData)

	bytesData, err := base64.StdEncoding.DecodeString(ttsResp.Audio)
	if err != nil {
		return nil, fmt.Errorf("failed to decode tts response: %w", err)
	}

	return bytesData, nil
}
