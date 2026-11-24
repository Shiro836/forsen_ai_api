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
			Description: "Update one or more text fields in the character card editor. ONLY call when the user explicitly asks to create, fill, or edit the card — NEVER call when answering questions, reviewing, or giving advice. Only pass fields you want to change.",
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
			Description: "Replace all message examples in the editor with a new list of realistic Twitch-chat request/response pairs. ONLY call when the user explicitly asks to create or edit examples — NEVER call for questions, reviews, or advice.",
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
			Description: "Append new message examples to the existing list without removing current ones. ONLY call when the user explicitly asks to add examples — NEVER call for questions, reviews, or advice.",
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
			Description: "Remove one message example by its 0-based index from the current editor list. ONLY call when the user explicitly asks to remove an example.",
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

func (a *Assistant) chatTools(webSearch bool) []llm.Tool {
	tools := make([]llm.Tool, 0, 5)
	if webSearch && a.search != nil {
		tools = append(tools, toolWebSearch)
	}
	tools = append(tools, toolUpdateFields, toolSetExamples, toolAddExamples, toolRemoveExample)
	return tools
}

func (a *Assistant) chatSystemPrompt(webSearch bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are an interactive character-card co-editor for BAJ AI, a Twitch livestream AI text-to-speech bot. Today's date is %s.\n\n", time.Now().Format("2006-01-02"))

	b.WriteString("How BAJ AI actually uses each card field at runtime:\n")
	b.WriteString("- char_name & char_description: Shown only in the character list and Twitch reward button; NOT sent to the roleplay model. Keep char_name short and recognizable, and char_description a punchy 1-sentence hook under 120 chars.\n")
	b.WriteString("- name: Injected into \"You are {name}...\" and \"About {name}: ...\". Use the natural name or handle the character goes by.\n")
	b.WriteString("- description: Injected as \"About {name}: {description}\". Core identity, lore, worldview, mannerisms, speech tics, slang, catchphrases, rivalries, pet peeves, and running gags.\n")
	b.WriteString("- personality: Injected as \"Personality: {personality}\". 5-8 sharp, high-signal comma-separated traits that anchor attitude (no filler like \"interesting, unique, complex\").\n")
	b.WriteString("- message_examples: THE MOST IMPORTANT FIELD FOR CHARACTER VOICE. Each pair is sent verbatim as real user/assistant few-shot conversation turns right before the viewer's prompt. The roleplay model directly mimics these turns' cadence, sentence length, humor, grammar/accent quirks, profanity level, and catchphrase density — examples shape how the character sounds far more than adjectives in the description.\n")
	b.WriteString("- first_message: Short 1-sentence in-character prompt shown before a viewer asks something (e.g. \"Ask your stupid question.\"). It is not injected as a roleplay turn, so never put essential lore only here.\n\n")

	b.WriteString("Principles for building a great BAJ AI character:\n")
	b.WriteString("- Do NOT waste description on boilerplate instructions (\"always stay in character\", \"never refuse\", \"never break character\", \"keep answers short\", \"you are an AI\"): the runtime system prompt already enforces uncensored commitment, staying in character, and spoken length.\n")
	b.WriteString("- Write for spoken roleplay, not Wikipedia: focus on how they think, talk, brag, complain, and joke — verbal crutches, accent/grammar quirks, strong opinions, ego, blind spots, and specific community memes or lore.\n")
	b.WriteString("- Craft 4-6 diverse message_examples: mix 1-2 in-domain prompts with 2-4 random Twitch chat prompts (absurd hypotheticals, life advice, hot takes like pineapple on pizza, viewer roasts or greetings). This teaches the model how the character handles anything chat redeems.\n")
	b.WriteString("- Match natural spoken rhythm: vary response lengths realistically (punchy 1-liners to 2-4 sentence rants) — if every example is a long essay, the character will ramble on stream.\n")
	b.WriteString("- 100% TTS-ready spoken text in responses: NO *stage directions*, NO (laughs)/[sighs], NO markdown or bullet lists, NO emojis, and avoid ALL CAPS words that TTS mispronounces.\n")
	b.WriteString("- Show confident commitment in examples: when asked something absurd, unknowable, or controversial, the character picks a funny, specific in-character stance rather than hedging.\n")
	b.WriteString("- Match Twitch banter: edgy, blunt, sarcastic, or dark humor and ordinary profanity fit when true to the character, but never put slurs or hate speech in the card (the stream filter bleeps them).\n\n")

	b.WriteString("CRITICAL — Decide whether to ADVISE or EDIT before calling any tool:\n")
	b.WriteString("- NEVER call update_fields, set_message_examples, add_message_examples, or remove_message_example when the user is asking a question, asking for a critique/review, asking \"how to improve...\", or brainstorming ideas (e.g. \"how to improve quality of this char?\", \"what's missing?\", \"what do you think?\", \"any suggestions?\", \"why does it sound off?\"). Reply in chat with specific, actionable advice tailored to the current card, and offer to apply the changes if they want.\n")
	b.WriteString("- ONLY call edit tools when the user explicitly instructs you to make changes (e.g. \"build\", \"create\", \"fill\", \"rewrite\", \"update\", \"add\", \"remove\", \"fix\", \"make it...\", \"translate\", \"apply those\", \"do it\").\n")
	b.WriteString("- When the user DOES ask you to edit, apply changes directly using update_fields and/or message-example tools rather than pasting rewritten fields in chat. When building a new character from scratch, populate all text fields via update_fields AND add 4-5 varied examples via set_message_examples.\n")
	if webSearch && a.search != nil {
		b.WriteString("- Use web_search whenever the user mentions a real person, streamer, game, meme, recent event, or niche character so facts, quotes, and recent lore are accurate instead of guessed (allowed for both edits and factual questions). Usually 1-2 focused queries are enough.\n")
	}
	b.WriteString("- Reply formatting: use plain text (no markdown bold/headings). After applying edits, keep confirmation brief (1-3 sentences). When answering questions or giving improvement advice, give 3-6 short, concrete bullet points referencing the current card and offer to apply them. Match the user's language.")
	return b.String()
}

// Chat runs one multi-step tool-calling turn against the current editor draft
// and returns the assistant's reply plus every field mutation performed.
func (a *Assistant) Chat(ctx context.Context, history []ChatMessage, userMessage string, current Draft, webSearch bool) (*ChatResult, error) {
	userMessage = strings.TrimSpace(userMessage)
	if userMessage == "" {
		return nil, ErrEmptyInput
	}

	working := current.Clean()
	changed := make(map[string]bool)
	var actions []ToolAction

	// Keep static system prompt + trimmed history first for llama.cpp prefix cache hits.
	msgs := make([]llm.Message, 0, len(history)+4)
	msgs = append(msgs, llm.Message{Role: "system", StrContent: a.chatSystemPrompt(webSearch)})

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

	tools := a.chatTools(webSearch)
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
			toolOut, action := a.runTool(ctx, tc.Function.Name, tc.Function.Arguments, &working, changed, webSearch)
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

func (a *Assistant) runTool(ctx context.Context, name, argsJSON string, draft *Draft, changed map[string]bool, webSearch bool) (string, *ToolAction) {
	switch name {
	case "web_search":
		if !webSearch || a.search == nil {
			return "error: web search is disabled", nil
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
