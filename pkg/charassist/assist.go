// Package charassist generates and refines character card fields and few-shot
// message examples using the local chat LLM with JSON-schema-constrained output.
package charassist

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"app/db"
	"app/pkg/archive"
	"app/pkg/llm"
	"app/pkg/websearch"
)

// ErrEmptyInput is returned when the caller supplies neither a prompt nor any
// existing character content to build from.
var ErrEmptyInput = errors.New("describe a character or fill in a field first")

// ChatClient is the minimal LLM capability charassist needs. *llm.Client
// satisfies it.
type ChatClient interface {
	ReqChat(ctx context.Context, req *llm.ChatRequest) (*llm.ChatResponse, error)
	GetModel() string
}

// Searcher queries the web for up-to-date lore and facts. *websearch.Client
// satisfies it.
type Searcher interface {
	Search(ctx context.Context, query string, count int) ([]websearch.Result, error)
}

// Draft mirrors the editable text fields of a character card.
type Draft struct {
	CharName        string              `json:"char_name"`
	CharDescription string              `json:"char_description"`
	Name            string              `json:"name"`
	Description     string              `json:"description"`
	Personality     string              `json:"personality"`
	FirstMessage    string              `json:"first_message"`
	MessageExamples []db.MessageExample `json:"message_examples"`
}

// HasContent reports whether any text field or non-empty example is filled in.
func (d Draft) HasContent() bool {
	if strings.TrimSpace(d.CharName) != "" ||
		strings.TrimSpace(d.CharDescription) != "" ||
		strings.TrimSpace(d.Name) != "" ||
		strings.TrimSpace(d.Description) != "" ||
		strings.TrimSpace(d.Personality) != "" ||
		strings.TrimSpace(d.FirstMessage) != "" {
		return true
	}
	for _, ex := range d.MessageExamples {
		if strings.TrimSpace(ex.Request) != "" || strings.TrimSpace(ex.Response) != "" {
			return true
		}
	}
	return false
}

// Clean returns a copy with trimmed strings and empty message examples removed.
func (d Draft) Clean() Draft {
	out := Draft{
		CharName:        strings.TrimSpace(d.CharName),
		CharDescription: strings.TrimSpace(d.CharDescription),
		Name:            strings.TrimSpace(d.Name),
		Description:     strings.TrimSpace(d.Description),
		Personality:     strings.TrimSpace(d.Personality),
		FirstMessage:    strings.TrimSpace(d.FirstMessage),
	}
	for _, ex := range d.MessageExamples {
		req := strings.TrimSpace(ex.Request)
		resp := strings.TrimSpace(ex.Response)
		if req == "" && resp == "" {
			continue
		}
		out.MessageExamples = append(out.MessageExamples, db.MessageExample{
			Request:  req,
			Response: resp,
		})
	}
	return out
}

const (
	// Card generation needs more output tokens than the 512-token RP cap.
	cardMaxTokens     = 2048
	examplesMaxTokens = 1024

	defaultExampleCount = 3
	maxExampleCount     = 5
)

var cardSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "char_name": { "type": "string" },
    "char_description": { "type": "string" },
    "name": { "type": "string" },
    "description": { "type": "string" },
    "personality": { "type": "string" },
    "first_message": { "type": "string" },
    "message_examples": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "request": { "type": "string" },
          "response": { "type": "string" }
        },
        "required": ["request", "response"],
        "additionalProperties": false
      },
      "minItems": 3,
      "maxItems": 5
    }
  },
  "required": ["char_name", "char_description", "name", "description", "personality", "first_message", "message_examples"],
  "additionalProperties": false
}`)

var examplesSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "examples": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "request": { "type": "string" },
          "response": { "type": "string" }
        },
        "required": ["request", "response"],
        "additionalProperties": false
      },
      "minItems": 1,
      "maxItems": 5
    }
  },
  "required": ["examples"],
  "additionalProperties": false
}`)

const cardSystemPrompt = `You design character cards for a Twitch livestream AI text-to-speech bot. Viewers redeem channel points to ask the character questions or prompts, and the character replies aloud via TTS.

Write vivid, funny, committed character cards — never bland, generic, or sanitized. Characters can be real public figures, streamers, internet memes, fictional characters, or original archetypes. For original characters, pick natural names that fit their culture and vibe rather than generic mashups.

Field guide:
- char_name: Short display name for the character list and Twitch reward button (e.g. "Forsen", "Gordon Ramsay", "Okayeg").
- char_description: Punchy 1-sentence blurb shown on the character card in the UI (under 140 characters).
- name: Name the AI uses as its identity (usually the same as char_name).
- description: Rich third-person character bio sent to the roleplay model (5-10 sentences). Cover identity, background, iconic quirks, demeanor, humor style, strong opinions, speech habits, catchphrases, slang, and community lore or memes so the model stays unmistakably in character. Write for spoken roleplay, not a dry encyclopedia entry.
- personality: Concise comma-separated list of 5-8 sharp personality traits.
- message_examples: 4 realistic viewer-request to character-response pairs. Mix in-domain questions with random Twitch chat prompts (life advice, hot takes, roasts, weird hypotheticals) so the model learns how the character reacts to anything. Responses are spoken aloud by TTS: write natural spoken lines (1-4 sentences) dripping with the character's voice and catchphrases — no stage directions, no *asterisks*, no markdown, no emojis, and avoid ALL CAPS words.
- first_message: Short in-character prompt inviting the viewer to ask something (1 short sentence, e.g. "Ask your stupid question.").

When given an existing card draft:
- If the user asks to edit, tweak, translate, or expand it, apply their changes while keeping unrelated details and voice intact.
- If the user asks for a completely different character, replace the card with that new character.
- If the user gives no extra instructions, keep what they already wrote and fill in or flesh out any missing fields consistently.

Match the language of the user's prompt or existing card if it is not English or asks for a specific language; otherwise write in English.
Return ONLY valid JSON matching the schema.`

const examplesSystemPrompt = `You write few-shot example exchanges for a Twitch livestream AI text-to-speech character card. Viewers redeem channel points to ask the character prompts, and the character answers aloud via TTS.

Rules:
- Stay 100% faithful to the character's bio, personality, slang, catchphrases, opinions, and humor.
- Mix in-domain questions with random Twitch chat prompts (life advice, hot takes, roasts, absurd hypotheticals) so the examples teach general voice, not just one narrow topic.
- Responses are spoken aloud by TTS: write natural spoken speech (1-4 sentences) with no stage directions, no *asterisks*, no markdown, no emojis, and avoid ALL CAPS words.
- Never repeat or lightly reword any existing examples — cover fresh scenarios and angles.
- Match the language of the character card.
- Return ONLY valid JSON matching the schema.`

// Assistant generates and refines character cards using the configured chat LLM.
type Assistant struct {
	client ChatClient
	search Searcher
}

// New creates a character Assistant backed by client and an optional web searcher.
func New(client ChatClient, search Searcher) *Assistant {
	return &Assistant{client: client, search: search}
}

// HasSearch reports whether web search is enabled on this assistant.
func (a *Assistant) HasSearch() bool {
	return a.search != nil
}

// Generate creates a complete character card draft from prompt and/or an
// existing draft. Either prompt or current may be empty, but not both.
func (a *Assistant) Generate(ctx context.Context, prompt string, current Draft) (*Draft, error) {
	prompt = strings.TrimSpace(prompt)
	current = current.Clean()
	if prompt == "" && !current.HasContent() {
		return nil, ErrEmptyInput
	}

	userMsg := buildCardUserPrompt(prompt, current)
	now := time.Now().UTC()
	raw, err := a.callSchema(
		archive.WithLLMKind(ctx, "char_assist"),
		fmt.Sprintf("Current date: %s (%s).\n\n%s", now.Format("January 2, 2006"), now.Format("2006-01-02"), cardSystemPrompt),
		userMsg,
		"character_card",
		cardSchema,
		cardMaxTokens,
	)
	if err != nil {
		return nil, err
	}

	var draft Draft
	if err := json.Unmarshal([]byte(stripCodeFence(raw)), &draft); err != nil {
		return nil, fmt.Errorf("parse character draft: %w", err)
	}

	draft = sanitizeDraft(draft)
	if !draft.HasContent() {
		return nil, fmt.Errorf("model returned an empty character draft")
	}
	return &draft, nil
}

// GenerateExamples creates count fresh message examples tailored to current,
// avoiding overlap with examples already on the draft.
func (a *Assistant) GenerateExamples(ctx context.Context, current Draft, count int) ([]db.MessageExample, error) {
	current = current.Clean()
	if !current.HasContent() {
		return nil, fmt.Errorf("%w: add a name or description before generating examples", ErrEmptyInput)
	}
	if count <= 0 {
		count = defaultExampleCount
	}
	if count > maxExampleCount {
		count = maxExampleCount
	}

	userMsg := buildExamplesUserPrompt(current, count)
	now := time.Now().UTC()
	raw, err := a.callSchema(
		archive.WithLLMKind(ctx, "char_assist_examples"),
		fmt.Sprintf("Current date: %s (%s).\n\n%s", now.Format("January 2, 2006"), now.Format("2006-01-02"), examplesSystemPrompt),
		userMsg,
		"character_examples",
		examplesSchema,
		examplesMaxTokens,
	)
	if err != nil {
		return nil, err
	}

	var parsed struct {
		Examples []db.MessageExample `json:"examples"`
	}
	if err := json.Unmarshal([]byte(stripCodeFence(raw)), &parsed); err != nil {
		return nil, fmt.Errorf("parse message examples: %w", err)
	}

	out := make([]db.MessageExample, 0, len(parsed.Examples))
	for _, ex := range parsed.Examples {
		req := strings.TrimSpace(ex.Request)
		resp := strings.TrimSpace(ex.Response)
		if req == "" || resp == "" {
			continue
		}
		out = append(out, db.MessageExample{Request: req, Response: resp})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("model returned no usable message examples")
	}
	return out, nil
}

func (a *Assistant) callSchema(ctx context.Context, systemPrompt, userPrompt, schemaName string, schema json.RawMessage, maxTokens int) (string, error) {
	temp, minP, rep := 0.8, 0.05, 1.05
	resp, err := a.client.ReqChat(ctx, &llm.ChatRequest{
		Model: a.client.GetModel(),
		Messages: []llm.Message{
			{Role: "system", StrContent: systemPrompt},
			{Role: "user", StrContent: userPrompt},
		},
		MaxTokens:         maxTokens,
		MinTokens:         1,
		Temperature:       &temp,
		MinP:              &minP,
		RepetitionPenalty: &rep,
		ChatTemplateKwargs: map[string]any{
			"enable_thinking": false,
		},
		ResponseFormat: &llm.ResponseFormat{
			Type: "json_schema",
			JSONSchema: &llm.JSONSchemaFormat{
				Name:   schemaName,
				Strict: true,
				Schema: schema,
			},
		},
	})
	if err != nil {
		return "", fmt.Errorf("llm character assist: %w", err)
	}
	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("llm character assist: no choices returned")
	}
	content := llm.StripThinking(resp.Choices[0].Message.Content)
	if content == "" {
		return "", fmt.Errorf("llm character assist: empty response")
	}
	return content, nil
}

func buildCardUserPrompt(prompt string, current Draft) string {
	if !current.HasContent() {
		return "Create a new character card based on this idea:\n" + prompt
	}

	curJSON, _ := json.MarshalIndent(current, "", "  ")
	var b strings.Builder
	b.WriteString("Current character card draft (JSON):\n")
	b.Write(curJSON)
	b.WriteString("\n\n")
	if prompt == "" {
		b.WriteString("Task: Keep the details already provided and fill in any empty or sparse fields to produce a complete, cohesive character card with 4 message examples.")
	} else {
		b.WriteString("User instruction:\n")
		b.WriteString(prompt)
	}
	return b.String()
}

func buildExamplesUserPrompt(current Draft, count int) string {
	var b strings.Builder
	name := current.Name
	if name == "" {
		name = current.CharName
	}
	if name != "" {
		fmt.Fprintf(&b, "Name: %s\n", name)
	}
	if current.CharDescription != "" {
		fmt.Fprintf(&b, "Short summary: %s\n", current.CharDescription)
	}
	if current.Description != "" {
		fmt.Fprintf(&b, "Description: %s\n", current.Description)
	}
	if current.Personality != "" {
		fmt.Fprintf(&b, "Personality: %s\n", current.Personality)
	}
	if current.FirstMessage != "" {
		fmt.Fprintf(&b, "First message: %s\n", current.FirstMessage)
	}
	if len(current.MessageExamples) > 0 {
		b.WriteString("\nExisting examples (do NOT repeat these topics or lines):\n")
		for i, ex := range current.MessageExamples {
			fmt.Fprintf(&b, "%d. Viewer: %s\n   Reply: %s\n", i+1, ex.Request, ex.Response)
		}
	}
	fmt.Fprintf(&b, "\nGenerate exactly %d new, distinct message examples for this character.", count)
	return b.String()
}

func sanitizeDraft(d Draft) Draft {
	d = d.Clean()
	if d.Name == "" {
		d.Name = d.CharName
	}
	if d.CharName == "" {
		d.CharName = d.Name
	}
	complete := make([]db.MessageExample, 0, len(d.MessageExamples))
	for _, ex := range d.MessageExamples {
		if ex.Request == "" || ex.Response == "" {
			continue
		}
		complete = append(complete, ex)
	}
	d.MessageExamples = complete
	return d
}

// stripCodeFence unwraps ```json ... ``` blocks in case a server ignores
// strict schema decoding and wraps the payload in markdown fences.
func stripCodeFence(s string) string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "```") {
		return s
	}
	if idx := strings.IndexByte(s, '\n'); idx >= 0 {
		s = s[idx+1:]
	}
	s = strings.TrimSuffix(strings.TrimSpace(s), "```")
	return strings.TrimSpace(s)
}
