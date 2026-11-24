package charassist

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"app/db"
	"app/pkg/archive"
	"app/pkg/llm"
	"app/pkg/websearch"
)

const (
	maxChatToolIterations = 8
	chatMaxTokens         = 2048
	chatMaxHistory        = 16
	searchSnippetRunes    = 400
	searchResultCount     = 5
)

// ChatMessage is a compact user or assistant turn retained across requests.
type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ToolAction describes one tool step performed during a chat turn for UI display.
type ToolAction struct {
	Kind  string
	Label string
}

// ChatResult is the outcome of one agentic assistant turn.
type ChatResult struct {
	Reply         string
	Draft         Draft
	ChangedFields map[string]bool
	Actions       []ToolAction
}

var (
	toolWebSearch = llm.Tool{
		Type: "function",
		Function: llm.ToolFunctionDef{
			Name:        "web_search",
			Description: "Search the web for up-to-date facts, lore, quotes, memes, or recent events about a person, streamer, game, or character.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"query": {"type": "string", "description": "Search query"}
				},
				"required": ["query"]
			}`),
		},
	}

	toolUpdateFields = llm.Tool{
		Type: "function",
		Function: llm.ToolFunctionDef{
			Name:        "update_fields",
			Description: "Update one or more text fields in the character card editor. Only pass fields you want to change.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"char_name": {"type": "string", "description": "Display name shown in character list and Twitch reward button"},
					"char_description": {"type": "string", "description": "Punchy 1-sentence card blurb (under 140 chars)"},
					"name": {"type": "string", "description": "Character identity name sent to the RP model"},
					"description": {"type": "string", "description": "Rich third-person bio (5-10 sentences) covering background, quirks, speech habits, catchphrases, humor, and lore"},
					"personality": {"type": "string", "description": "Concise comma-separated list of 5-8 sharp traits"},
					"first_message": {"type": "string", "description": "Short in-character prompt inviting the viewer to ask something"}
				}
			}`),
		},
	}

	toolSetExamples = llm.Tool{
		Type: "function",
		Function: llm.ToolFunctionDef{
			Name:        "set_message_examples",
			Description: "Replace all message examples in the editor with a new list of realistic Twitch-chat request/response pairs.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"examples": {
						"type": "array",
						"items": {
							"type": "object",
							"properties": {
								"request": {"type": "string"},
								"response": {"type": "string"}
							},
							"required": ["request", "response"]
						}
					}
				},
				"required": ["examples"]
			}`),
		},
	}

	toolAddExamples = llm.Tool{
		Type: "function",
		Function: llm.ToolFunctionDef{
			Name:        "add_message_examples",
			Description: "Append new message examples to the existing list without removing current ones.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"examples": {
						"type": "array",
						"items": {
							"type": "object",
							"properties": {
								"request": {"type": "string"},
								"response": {"type": "string"}
							},
							"required": ["request", "response"]
						}
					}
				},
				"required": ["examples"]
			}`),
		},
	}

	toolRemoveExample = llm.Tool{
		Type: "function",
		Function: llm.ToolFunctionDef{
			Name:        "remove_message_example",
			Description: "Remove one message example by its 0-based index from the current editor list.",
			Parameters: json.RawMessage(`{
				"type": "object",
				"properties": {
					"index": {"type": "integer", "minimum": 0}
				},
				"required": ["index"]
			}`),
		},
	}
)

func (a *Assistant) chatTools() []llm.Tool {
	tools := make([]llm.Tool, 0, 5)
	if a.search != nil {
		tools = append(tools, toolWebSearch)
	}
	tools = append(tools, toolUpdateFields, toolSetExamples, toolAddExamples, toolRemoveExample)
	return tools
}

func (a *Assistant) chatSystemPrompt() string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are an interactive character-card co-editor for a Twitch livestream AI text-to-speech bot. Today's date is %s.\n\n", time.Now().Format("2006-01-02"))
	b.WriteString("Viewers redeem channel points to ask a character questions or prompts, and the character replies aloud via TTS. Write vivid, funny, committed character cards — never bland, generic, or sanitized. Characters can be real public figures, streamers, internet memes, fictional characters, or original archetypes.\n\n")
	b.WriteString("How to use tools:\n")
	if a.search != nil {
		b.WriteString("- Use web_search whenever the user mentions a real person, streamer, game, meme, recent event, or niche character whose up-to-date lore, catchphrases, or facts you should verify.\n")
	}
	b.WriteString("- Whenever the user asks to create, fill, rewrite, translate, or change the character, ALWAYS apply the edits directly using update_fields and/or message-example tools rather than only pasting text in chat.\n")
	b.WriteString("- When creating a brand-new character from scratch, fill all text fields via update_fields AND add 4 strong message examples via set_message_examples.\n")
	b.WriteString("- When the user only asks a question or wants brainstorming without editing, just answer in chat without calling edit tools.\n")
	b.WriteString("- For message examples: mix in-domain questions with random Twitch chat prompts (hot takes, life advice, roasts, weird hypotheticals). Responses are read aloud by TTS: natural spoken speech (1-4 sentences), no stage directions, no *asterisks*, no markdown, no emojis, avoid ALL CAPS words.\n")
	b.WriteString("- Keep your final chat reply brief and direct (1-3 sentences). Match the user's language.")
	return b.String()
}

// Chat runs one multi-step tool-calling turn against the current editor draft
// and returns the assistant's reply plus every field mutation performed.
func (a *Assistant) Chat(ctx context.Context, history []ChatMessage, userMessage string, current Draft) (*ChatResult, error) {
	userMessage = strings.TrimSpace(userMessage)
	if userMessage == "" {
		return nil, ErrEmptyInput
	}

	working := current.Clean()
	changed := make(map[string]bool)
	var actions []ToolAction

	// Keep static system prompt + trimmed history first for llama.cpp prefix cache hits.
	msgs := make([]llm.Message, 0, len(history)+4)
	msgs = append(msgs, llm.Message{Role: "system", StrContent: a.chatSystemPrompt()})

	if len(history) > chatMaxHistory {
		history = history[len(history)-chatMaxHistory:]
	}
	for _, h := range history {
		if h.Role != "user" && h.Role != "assistant" {
			continue
		}
		if strings.TrimSpace(h.Content) == "" {
			continue
		}
		msgs = append(msgs, llm.Message{Role: h.Role, StrContent: h.Content})
	}
	msgs = append(msgs, llm.Message{Role: "user", StrContent: formatChatUserTurn(working, userMessage)})

	tools := a.chatTools()
	chatCtx := archive.WithLLMKind(ctx, "char_assist_chat")
	temp, minP, rep := 0.7, 0.05, 1.05

	var reply string
	for iter := range maxChatToolIterations {
		resp, err := a.client.ReqChat(chatCtx, &llm.ChatRequest{
			Model:             a.client.GetModel(),
			Messages:          msgs,
			MaxTokens:         chatMaxTokens,
			MinTokens:         1,
			Temperature:       &temp,
			MinP:              &minP,
			RepetitionPenalty: &rep,
			Tools:             tools,
			ChatTemplateKwargs: map[string]any{
				"enable_thinking": false,
			},
		})
		if err != nil {
			return nil, fmt.Errorf("chat iteration %d: %w", iter, err)
		}
		if len(resp.Choices) == 0 {
			return nil, fmt.Errorf("chat iteration %d: no choices returned", iter)
		}

		msg := resp.Choices[0].Message
		if len(msg.ToolCalls) == 0 {
			reply = llm.StripThinking(msg.Content)
			break
		}

		msgs = append(msgs, llm.Message{
			Role:       "assistant",
			StrContent: msg.Content,
			ToolCalls:  msg.ToolCalls,
		})

		for _, tc := range msg.ToolCalls {
			toolOut, action := a.runTool(ctx, tc.Function.Name, tc.Function.Arguments, &working, changed)
			if action != nil {
				actions = append(actions, *action)
			}
			msgs = append(msgs, llm.Message{
				Role:       "tool",
				ToolCallID: tc.ID,
				StrContent: toolOut,
			})
		}
	}

	if reply == "" {
		if len(actions) > 0 {
			reply = "Done."
		} else {
			return nil, fmt.Errorf("assistant returned an empty reply")
		}
	}

	return &ChatResult{
		Reply:         reply,
		Draft:         working,
		ChangedFields: changed,
		Actions:       actions,
	}, nil
}

func formatChatUserTurn(current Draft, userMessage string) string {
	var b strings.Builder
	b.WriteString("Current editor state (JSON):\n")
	if !current.HasContent() {
		b.WriteString("{}\n\n")
	} else {
		raw, _ := json.Marshal(current)
		b.Write(raw)
		b.WriteString("\n\n")
	}
	b.WriteString("User: ")
	b.WriteString(userMessage)
	return b.String()
}

func (a *Assistant) runTool(ctx context.Context, name, argsJSON string, draft *Draft, changed map[string]bool) (string, *ToolAction) {
	switch name {
	case "web_search":
		if a.search == nil {
			return "error: web search is not enabled", nil
		}
		var args struct {
			Query string `json:"query"`
		}
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			return fmt.Sprintf("error: invalid arguments: %v", err), nil
		}
		query := strings.TrimSpace(args.Query)
		if query == "" {
			return "error: empty search query", nil
		}
		results, err := a.search.Search(ctx, query, searchResultCount)
		if err != nil {
			return fmt.Sprintf("error: search failed: %v", err), &ToolAction{Kind: "search", Label: "search failed: " + query}
		}
		return websearch.Format(results, searchSnippetRunes), &ToolAction{Kind: "search", Label: "searched: " + query}

	case "update_fields":
		var args struct {
			CharName        *string `json:"char_name"`
			CharDescription *string `json:"char_description"`
			Name            *string `json:"name"`
			Description     *string `json:"description"`
			Personality     *string `json:"personality"`
			FirstMessage    *string `json:"first_message"`
		}
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			return fmt.Sprintf("error: invalid arguments: %v", err), nil
		}
		var updated []string
		apply := func(key, label string, ptr *string, dst *string) {
			if ptr == nil {
				return
			}
			*dst = strings.TrimSpace(*ptr)
			changed[key] = true
			updated = append(updated, label)
		}
		apply("char_name", "card name", args.CharName, &draft.CharName)
		apply("char_description", "card bio", args.CharDescription, &draft.CharDescription)
		apply("name", "name", args.Name, &draft.Name)
		apply("description", "description", args.Description, &draft.Description)
		apply("personality", "personality", args.Personality, &draft.Personality)
		apply("first_message", "first message", args.FirstMessage, &draft.FirstMessage)

		if len(updated) == 0 {
			return "no fields provided", nil
		}
		return "updated: " + strings.Join(updated, ", "), &ToolAction{
			Kind:  "edit",
			Label: "updated " + strings.Join(updated, ", "),
		}

	case "set_message_examples":
		var args struct {
			Examples []db.MessageExample `json:"examples"`
		}
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			return fmt.Sprintf("error: invalid arguments: %v", err), nil
		}
		cleaned := cleanExamples(args.Examples)
		draft.MessageExamples = cleaned
		changed["message_examples"] = true
		return fmt.Sprintf("set %d message examples", len(cleaned)), &ToolAction{
			Kind:  "examples",
			Label: fmt.Sprintf("set %d examples", len(cleaned)),
		}

	case "add_message_examples":
		var args struct {
			Examples []db.MessageExample `json:"examples"`
		}
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			return fmt.Sprintf("error: invalid arguments: %v", err), nil
		}
		added := cleanExamples(args.Examples)
		if len(added) == 0 {
			return "error: no valid examples provided", nil
		}
		draft.MessageExamples = append(draft.MessageExamples, added...)
		changed["message_examples"] = true
		return fmt.Sprintf("added %d examples (total %d)", len(added), len(draft.MessageExamples)), &ToolAction{
			Kind:  "examples",
			Label: fmt.Sprintf("added %d examples", len(added)),
		}

	case "remove_message_example":
		var args struct {
			Index int `json:"index"`
		}
		if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
			return fmt.Sprintf("error: invalid arguments: %v", err), nil
		}
		if args.Index < 0 || args.Index >= len(draft.MessageExamples) {
			return fmt.Sprintf("error: index %d out of range (0..%d)", args.Index, len(draft.MessageExamples)-1), nil
		}
		draft.MessageExamples = append(draft.MessageExamples[:args.Index], draft.MessageExamples[args.Index+1:]...)
		changed["message_examples"] = true
		return fmt.Sprintf("removed example %d (remaining %d)", args.Index, len(draft.MessageExamples)), &ToolAction{
			Kind:  "examples",
			Label: fmt.Sprintf("removed example #%d", args.Index+1),
		}

	default:
		return fmt.Sprintf("error: unknown tool %q", name), nil
	}
}

func cleanExamples(in []db.MessageExample) []db.MessageExample {
	out := make([]db.MessageExample, 0, len(in))
	for _, ex := range in {
		req := strings.TrimSpace(ex.Request)
		resp := strings.TrimSpace(ex.Response)
		if req == "" || resp == "" {
			continue
		}
		out = append(out, db.MessageExample{Request: req, Response: resp})
	}
	return out
}
