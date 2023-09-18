package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
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

	// fmt.Println(string(data))

	resp, err := httpClient.Post(ttsUrl, "application/json", bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("failed to post to tts server: %w", err)
	}

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
