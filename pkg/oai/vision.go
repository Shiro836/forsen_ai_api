package oai

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/shared"
)

// Image is one inline image sent with a vision request.
type Image struct {
	MIME string
	Data []byte
}

// visionSchemaName labels the response format. The API requires a name; nothing
// reads it back.
const visionSchemaName = "vision_response"

// AskVisionJSON runs a single-turn prompt+images completion constrained to the
// caller's JSON schema and returns the assistant's raw text. The schema is what
// enforces the format: llama-server accepts json_object mode and ignores it —
// measured, a request for prose under json_object came back as prose — while a
// json_schema compiles to a sampling grammar. On compatible endpoints it also
// disables the chat template's thinking block: qwen36 reasoning about an image
// costs tens of seconds and does not improve the verdict.
func (c *Client) AskVisionJSON(ctx context.Context, prompt string, images []Image, schema json.RawMessage, temperature float64) (string, error) {
	parts := make([]openai.ChatCompletionContentPartUnionParam, 0, len(images)+1)
	parts = append(parts, openai.TextContentPart(prompt))
	for _, img := range images {
		if len(img.Data) == 0 {
			continue
		}
		mime := img.MIME
		if mime == "" {
			mime = "image/png"
		}
		parts = append(parts, openai.ImageContentPart(openai.ChatCompletionContentPartImageImageURLParam{
			URL: fmt.Sprintf("data:%s;base64,%s", mime, base64.StdEncoding.EncodeToString(img.Data)),
		}))
	}

	params := openai.ChatCompletionNewParams{
		Model:    shared.ChatModel(c.Model),
		Messages: []openai.ChatCompletionMessageParamUnion{openai.UserMessage(parts)},
		ResponseFormat: openai.ChatCompletionNewParamsResponseFormatUnion{
			OfJSONSchema: &shared.ResponseFormatJSONSchemaParam{
				JSONSchema: shared.ResponseFormatJSONSchemaJSONSchemaParam{
					Name:   visionSchemaName,
					Schema: schema,
				},
			},
		},
	}
	c.applyTuning(&params, temperature)

	var reqOpts []option.RequestOption
	if c.dialect == dialectCompatible {
		reqOpts = append(reqOpts, option.WithJSONSet("chat_template_kwargs", map[string]bool{"enable_thinking": false}))
	}

	resp, err := c.API.Chat.Completions.New(ctx, params, reqOpts...)
	if err != nil {
		return "", fmt.Errorf("oai vision chat: %w", err)
	}
	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("oai vision: no choices returned")
	}
	return resp.Choices[0].Message.Content, nil
}
