package rvc

import (
	"app/tools"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

type Config struct {
	URL string `yaml:"url"`
}

type Client struct {
	httpClient HTTPClient
	cfg        *Config
}

type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

func New(httpClient HTTPClient, cfg *Config) *Client {
	return &Client{
		httpClient: httpClient,
		cfg:        cfg,
	}
}

type rvcRequest struct {
	Inputfile    string  `json:"input_file"`
	ModelName    string  `json:"model_name"`
	IndexPath    string  `json:"index_path"`
	F0upKey      int     `json:"f0up_key"`
	F0method     string  `json:"f0method"`
	IndexRate    float64 `json:"index_rate"`
	Device       string  `json:"device"`
	IsHalf       bool    `json:"is_half"`
	FilterRadius int     `json:"filter_radius"`
	ResampleSr   int     `json:"resample_sr"`
	RmsMixRate   float64 `json:"rms_mix_rate"`
	Protect      float64 `json:"protect"`
}

type rvcResp struct {
	Audio string `json:"audio"`
}

func (c *Client) Rvc(ctx context.Context, voice string, audio []byte) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	rvcReq := &rvcRequest{
		Inputfile:    base64.StdEncoding.EncodeToString(audio),
		ModelName:    voice,
		IndexPath:    "assets/weights/" + voice + ".index",
		F0upKey:      0,
		F0method:     "rmvpe",
		IndexRate:    0.66,
		Device:       "cuda",
		IsHalf:       false,
		FilterRadius: 3,
		ResampleSr:   24000,
		RmsMixRate:   1,
		Protect:      0.33,
	}
	rvcReqData, err := json.Marshal(&rvcReq)
	if err != nil {
		return nil, fmt.Errorf("failed to create rvcRequest: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.URL, bytes.NewReader(rvcReqData))
	if err != nil {
		return nil, fmt.Errorf("failed to create http rvc request: %w", err)
	}
	req.Header.Add("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to do rvc request: %w", err)
	}
	defer tools.DrainAndClose(resp.Body)

	respData, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read body: %w", err)
	}

	if resp.StatusCode > 299 {
		return nil, fmt.Errorf("status code %d, err - %s", resp.StatusCode, string(respData))
	}

	// fmt.Println(string(respData))

	rvcResp := &rvcResp{}
	err = json.Unmarshal(respData, &rvcResp)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal tts resp data: %w", err)
	}

	// fmt.Println(respData)

	bytesData, err := base64.StdEncoding.DecodeString(rvcResp.Audio)
	if err != nil {
		return nil, fmt.Errorf("failed to decode tts response: %w", err)
	}

	return bytesData, nil
}
