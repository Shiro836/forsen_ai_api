package oai

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"app/pkg/llm"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/shared"
)

type Client struct {
	API       openai.Client
	Model     string
	MaxTokens int64
}

func New(apiKey, baseURL, model string, maxTokens int) *Client {
	opts := []option.RequestOption{option.WithAPIKey(apiKey)}
	if baseURL != "" {
		opts = append(opts, option.WithBaseURL(baseURL))
	}
	return &Client{
		API:       openai.NewClient(opts...),
		Model:     model,
		MaxTokens: int64(maxTokens),
	}
}

func flattenText(m llm.Message) string {
	if m.StrContent != "" {
		return m.StrContent
	}
	var b strings.Builder
	for _, c := range m.Content {
		if c.Type == "text" && c.Text != "" {
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			b.WriteString(c.Text)
		}
	}
	return b.String()
}

// AskGuided embeds the schema in the prompt and forces json_object mode —
// OpenAI-compatible endpoints have no vLLM-style enum-constrained decoding.
func (c *Client) AskGuided(ctx context.Context, messages []llm.Message, schema json.RawMessage, temperature float64) (string, error) {
	oaMessages := make([]openai.ChatCompletionMessageParamUnion, 0, len(messages)+1)
	for _, m := range messages {
		text := flattenText(m)
		switch m.Role {
		case "system":
			oaMessages = append(oaMessages, openai.SystemMessage(text))
		case "assistant":
			oaMessages = append(oaMessages, openai.AssistantMessage(text))
		default:
			oaMessages = append(oaMessages, openai.UserMessage(text))
		}
	}
	if len(schema) > 0 {
		oaMessages = append(oaMessages, openai.SystemMessage(
			"Respond ONLY with a JSON object conforming to this JSON schema (no prose, no code fences):\n"+string(schema)))
	}

	params := openai.ChatCompletionNewParams{
		Model:    shared.ChatModel(c.Model),
		Messages: oaMessages,
		ResponseFormat: openai.ChatCompletionNewParamsResponseFormatUnion{
			OfJSONObject: &shared.ResponseFormatJSONObjectParam{},
		},
		Temperature: openai.Float(temperature),
	}
	if c.MaxTokens > 0 {
		params.MaxTokens = openai.Int(c.MaxTokens)
	}

	resp, err := c.API.Chat.Completions.New(ctx, params)
	if err != nil {
		return "", fmt.Errorf("oai chat: %w", err)
	}
	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("oai: no choices returned")
	}
	return resp.Choices[0].Message.Content, nil
}

// Ask runs a plain (non-JSON) chat completion and returns the assistant's raw
// text. Use this when the model must return free-form or verbatim text (e.g.
// content annotated in place); AskGuided forces json_object mode, whose escaping
// would corrupt such output.
func (c *Client) Ask(ctx context.Context, messages []llm.Message, temperature float64) (string, error) {
	oaMessages := make([]openai.ChatCompletionMessageParamUnion, 0, len(messages))
	for _, m := range messages {
		text := flattenText(m)
		switch m.Role {
		case "system":
			oaMessages = append(oaMessages, openai.SystemMessage(text))
		case "assistant":
			oaMessages = append(oaMessages, openai.AssistantMessage(text))
		default:
			oaMessages = append(oaMessages, openai.UserMessage(text))
		}
	}

	params := openai.ChatCompletionNewParams{
		Model:       shared.ChatModel(c.Model),
		Messages:    oaMessages,
		Temperature: openai.Float(temperature),
	}
	if c.MaxTokens > 0 {
		params.MaxTokens = openai.Int(c.MaxTokens)
	}

	// Span tagging is deterministic; reasoning only adds latency. deepseek
	// models think by default, so disable it explicitly.
	resp, err := c.API.Chat.Completions.New(ctx, params,
		option.WithJSONSet("thinking", map[string]string{"type": "disabled"}))
	if err != nil {
		return "", fmt.Errorf("oai chat: %w", err)
	}
	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("oai: no choices returned")
	}
	return resp.Choices[0].Message.Content, nil
}
