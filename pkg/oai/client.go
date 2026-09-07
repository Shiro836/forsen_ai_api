package oai

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"app/pkg/archive"
	"app/pkg/llm"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/shared"
)

// dialect is the request shape an endpoint accepts. GPT-5.x rejects any
// temperature but the default and rejects max_tokens; DeepSeek takes the
// classic parameters plus a thinking block.
type dialect int

const (
	dialectCompatible dialect = iota
	dialectOpenAI
)

// openai-go v1.12.0 predates the "none" tier that GPT-5.x accepts.
const reasoningNone shared.ReasoningEffort = "none"

type Client struct {
	API       openai.Client
	Model     string
	MaxTokens int64

	dialect dialect
}

// New builds a client for cfg. cfg.Timeout is a hard cap per request: the SDK
// does not retry past it. Zero leaves requests uncapped.
func New(cfg *llm.Config) *Client {
	opts := []option.RequestOption{option.WithAPIKey(cfg.AccessToken)}
	if cfg.URL != "" {
		opts = append(opts, option.WithBaseURL(cfg.URL))
	}
	if cfg.Timeout > 0 {
		opts = append(opts, option.WithRequestTimeout(cfg.Timeout))
	}
	return &Client{
		API:       openai.NewClient(opts...),
		Model:     cfg.Model,
		MaxTokens: int64(cfg.MaxTokens),
		dialect:   dialectFor(cfg.URL),
	}
}

func dialectFor(baseURL string) dialect {
	if baseURL == "" || strings.Contains(baseURL, "api.openai.com") {
		return dialectOpenAI
	}
	return dialectCompatible
}

// applyTuning sets sampling and length in the dialect's spelling. Reasoning is
// pinned off: measured on gpt-5.6-luna it tripled latency, spent ~10x the output
// tokens, and made spans less precise.
func (c *Client) applyTuning(params *openai.ChatCompletionNewParams, temperature float64) {
	if c.dialect == dialectOpenAI {
		if c.MaxTokens > 0 {
			params.MaxCompletionTokens = openai.Int(c.MaxTokens)
		}
		params.ReasoningEffort = reasoningNone
		return
	}

	params.Temperature = openai.Float(temperature)
	if c.MaxTokens > 0 {
		params.MaxTokens = openai.Int(c.MaxTokens)
	}
}

// NewParams builds chat params with model, length and sampling already in the
// dialect's spelling. Callers needing tools or a response format set them on
// the result rather than assembling ChatCompletionNewParams themselves.
func (c *Client) NewParams(messages []openai.ChatCompletionMessageParamUnion, temperature float64) openai.ChatCompletionNewParams {
	params := openai.ChatCompletionNewParams{
		Model:    shared.ChatModel(c.Model),
		Messages: messages,
	}
	c.applyTuning(&params, temperature)
	return params
}

func (c *Client) convertMessages(messages []llm.Message, extra int) []openai.ChatCompletionMessageParamUnion {
	out := make([]openai.ChatCompletionMessageParamUnion, 0, len(messages)+extra)
	for _, m := range messages {
		text := flattenText(m)
		switch m.Role {
		case "system":
			out = append(out, openai.SystemMessage(text))
		case "assistant":
			out = append(out, openai.AssistantMessage(text))
		default:
			out = append(out, openai.UserMessage(text))
		}
	}
	return out
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
	oaMessages := c.convertMessages(messages, 1)
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
	}
	c.applyTuning(&params, temperature)

	return c.complete(ctx, params)
}

// Ask runs a plain (non-JSON) chat completion and returns the assistant's raw
// text. Use this when the model must return free-form or verbatim text (e.g.
// content annotated in place); AskGuided forces json_object mode, whose escaping
// would corrupt such output.
func (c *Client) Ask(ctx context.Context, messages []llm.Message, temperature float64) (string, error) {
	params := openai.ChatCompletionNewParams{
		Model:    shared.ChatModel(c.Model),
		Messages: c.convertMessages(messages, 0),
	}
	c.applyTuning(&params, temperature)

	// deepseek models think by default and have no request field for it.
	var reqOpts []option.RequestOption
	if c.dialect == dialectCompatible {
		reqOpts = append(reqOpts, option.WithJSONSet("thinking", map[string]string{"type": "disabled"}))
	}

	return c.complete(ctx, params, reqOpts...)
}

// complete runs the request and records it for the message archive. Options
// applied through reqOpts (the deepseek thinking switch) are not part of the
// recorded body.
func (c *Client) complete(ctx context.Context, params openai.ChatCompletionNewParams, reqOpts ...option.RequestOption) (string, error) {
	body, _ := json.Marshal(params)
	start := time.Now()
	call := archive.LLMCall{Model: c.Model, Endpoint: "oai", Request: body, At: start.UnixMilli()}
	defer func() {
		call.LatencyMs = int(time.Since(start).Milliseconds())
		archive.RecordLLM(ctx, call)
	}()

	resp, err := c.API.Chat.Completions.New(ctx, params, reqOpts...)
	if err != nil {
		call.Error = err.Error()
		return "", fmt.Errorf("oai chat: %w", err)
	}
	if resp.Model != "" {
		call.Model = resp.Model
	}
	call.PromptTok = int(resp.Usage.PromptTokens)
	call.OutputTok = int(resp.Usage.CompletionTokens)
	if len(resp.Choices) == 0 {
		call.Error = "no choices returned"
		return "", fmt.Errorf("oai: no choices returned")
	}
	call.Response = resp.Choices[0].Message.Content
	return call.Response, nil
}
