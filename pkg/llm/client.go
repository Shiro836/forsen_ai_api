package llm

import (
	"app/pkg/tools"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
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
	MinTokens int    `yaml:"min_tokens"`
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

type ImageURL struct {
	URL string `json:"url"`
}

type MessageContent struct {
	Type     string    `json:"type"`
	Text     string    `json:"text,omitempty"`
	ImageURL *ImageURL `json:"image_url,omitempty"`
}

type Message struct {
	Role    string           `json:"role"`
	Content []MessageContent `json:"content"`
}

type StructuredOutputs struct {
	JSON json.RawMessage `json:"json,omitempty"`
}

type ChatRequest struct {
	Model             string             `json:"model"`
	Messages          []Message          `json:"messages"`
	MaxTokens         int                `json:"max_tokens"`
	MinTokens         int                `json:"min_tokens"`
	Stop              []string           `json:"stop,omitempty"`
	StructuredOutputs *StructuredOutputs `json:"structured_outputs,omitempty"`
	Temperature       *float64           `json:"temperature,omitempty"`
}

type ChatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

type Attachment struct {
	Data        []byte
	ContentType string
}

type aiReq struct {
	Model            string   `json:"model"`
	Prompt           string   `json:"prompt"`
	MaxTokens        int      `json:"max_tokens"`
	Temperature      float64  `json:"temperature"`
	FrequencyPenalty float64  `json:"frequency_penalty"`
	Stop             []string `json:"stop"`
}

type aiChoice struct {
	Text    string `json:"text"`
	Message struct {
		Content string `json:"content"`
	} `json:"message"`
}

type aiResp struct {
	Choices []aiChoice `json:"choices"`
}

func (c *Client) reqAi(ctx context.Context, req *aiReq) ([]string, error) {
	data, err := json.Marshal(&req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal ai request struct: %w", err)
	}

	url := strings.TrimRight(c.cfg.URL, "/") + "/v1/completions"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("failed to create ai http request: %w", err)
	}

	request.Header.Set("Content-Type", "application/json")
	if c.cfg.AccessToken != "" {
		request.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.cfg.AccessToken))
	}

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

	out := make([]string, 0, len(resp.Choices))
	for _, ch := range resp.Choices {
		if ch.Text != "" {
			out = append(out, ch.Text)
			continue
		}
		if ch.Message.Content != "" {
			out = append(out, ch.Message.Content)
			continue
		}
		out = append(out, "")
	}
	return out, nil
}

func (c *Client) AskMessages(ctx context.Context, messages []Message, images []Attachment) (string, error) {
	if len(images) > 0 {
		idx := -1
		for i := len(messages) - 1; i >= 0; i-- {
			if messages[i].Role == "user" {
				idx = i
				break
			}
		}

		if idx < 0 {
			messages = append(messages, Message{Role: "user", Content: []MessageContent{}})
			idx = len(messages) - 1
		}

		for _, att := range images {
			if len(att.Data) == 0 {
				continue
			}
			ctype := att.ContentType
			if ctype == "" {
				ctype = "image/png"
			}
			encoded := base64.StdEncoding.EncodeToString(att.Data)
			imageURL := fmt.Sprintf("data:%s;base64,%s", ctype, encoded)
			messages[idx].Content = append(messages[idx].Content, MessageContent{Type: "image_url", ImageURL: &ImageURL{URL: imageURL}})
		}
	}

	req := &ChatRequest{
		Model:     c.cfg.Model,
		Messages:  messages,
		MaxTokens: c.cfg.MaxTokens,
		MinTokens: c.cfg.MinTokens,
		Stop:      []string{"###", "<START>", "<END>"},
	}

	resp, err := c.reqChat(ctx, req)
	if err != nil {
		return "", fmt.Errorf("failed to do chat request: %w", err)
	}

	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("no choices returned from AI")
	}

	return resp.Choices[0].Message.Content, nil
}

func (c *Client) Ask(ctx context.Context, prompt string) (string, error) {
	variants, err := c.reqAi(ctx, &aiReq{
		Model:            c.cfg.Model,
		Prompt:           prompt,
		MaxTokens:        c.cfg.MaxTokens,
		Temperature:      0.5,
		FrequencyPenalty: 1.1,
		Stop:             []string{"###", "<START>", "<END>"},
	})
	if err != nil {
		return "", fmt.Errorf("failed to do ai request: %w", err)
	}

	longest := ""
	for _, v := range variants {
		if len(v) > len(longest) {
			longest = v
		}
	}
	return longest, nil
}

func (c *Client) AskGuided(ctx context.Context, messages []Message, schema json.RawMessage, temperature float64) (string, error) {
	req := &ChatRequest{
		Model:     c.cfg.Model,
		Messages:  messages,
		MaxTokens: c.cfg.MaxTokens,
		MinTokens: c.cfg.MinTokens,
		StructuredOutputs: &StructuredOutputs{
			JSON: schema,
		},
		Temperature: &temperature,
	}

	resp, err := c.reqChat(ctx, req)
	if err != nil {
		return "", fmt.Errorf("failed to do guided chat request: %w", err)
	}

	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("no choices returned from AI")
	}

	return resp.Choices[0].Message.Content, nil
}

func (c *Client) reqChat(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
	data, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal chat request struct: %w", err)
	}

	chatURL := strings.TrimRight(c.cfg.URL, "/") + "/v1/chat/completions"

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, chatURL, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("failed to create chat http request: %w", err)
	}

	request.Header.Set("Content-Type", "application/json")
	if c.cfg.AccessToken != "" {
		request.Header.Set("Authorization", fmt.Sprintf("Bearer %s", c.cfg.AccessToken))
	}

	start := time.Now()

	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("failed to do chat http request: %w", err)
	}
	defer tools.DrainAndClose(response.Body)

	responseData, err := io.ReadAll(response.Body)
	if err != nil {
		metrics.LLMErrors.WithLabelValues("500").Inc()
		return nil, fmt.Errorf("failed to read chat http response body: %w", err)
	}

	if response.StatusCode != http.StatusOK {
		metrics.LLMErrors.WithLabelValues(strconv.Itoa(response.StatusCode)).Inc()
		return nil, fmt.Errorf("unexpected status code: %d, body: %s", response.StatusCode, string(responseData))
	}

	var resp ChatResponse

	if err := json.Unmarshal(responseData, &resp); err != nil {
		metrics.LLMErrors.WithLabelValues("500").Inc()
		return nil, fmt.Errorf("failed to unmarshal chat http response body: %w", err)
	}

	metrics.LLMQueryTime.Observe(time.Since(start).Seconds())

	return &resp, nil
}
