package charassist

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"app/pkg/archive"
)

const (
	maxTips       = 4
	tipsMaxTokens = 384
)

// Tip is a clickable quick-prompt chip that prefills the chat composer.
type Tip struct {
	Label  string `json:"label"`
	Prompt string `json:"prompt"`
}

var (
	ttsStageRe = regexp.MustCompile(`\*[^*\n]{1,40}\*|\[[^\]\n]{1,30}\]|\([^)\n]{1,25}\)|` + "```")

	tipsSchema = json.RawMessage(`{
  "type": "object",
  "properties": {
    "tips": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "label": { "type": "string" },
          "prompt": { "type": "string" }
        },
        "required": ["label", "prompt"],
        "additionalProperties": false
      },
      "minItems": 3,
      "maxItems": 4
    }
  },
  "required": ["tips"],
  "additionalProperties": false
}`)
)

const tipsSystemPrompt = `You suggest 3-4 short next-step editing prompts for a user building a BAJ AI Twitch livestream TTS character card.

Context on BAJ AI character cards:
- message_examples are few-shot conversation turns and have the biggest effect on voice, cadence, humor, and length; great cards have 4-6 examples mixing in-domain prompts with random Twitch chat prompts (hot takes, roasts, life advice).
- Responses are spoken aloud by TTS: no *stage directions*, brackets, markdown, emojis, or ALL CAPS.
- description should focus on spoken voice, catchphrases, lore, quirks, and opinions — not boilerplate "stay in character" rules.

Rules:
- Make every suggestion concrete and specific to THIS character, its current gaps, and any recent chat messages.
- label: ultra-short button text (2-4 words, max 28 chars), e.g. "Add Minecraft roasts", "Fix TTS formatting", "More TI 2025 lore".
- prompt: a clear 1-sentence instruction the user can review or tweak before hitting Send.
- Match the language of the character card or conversation.
- Return ONLY valid JSON matching the schema.`

// GenerateTips produces 3-4 strict-schema follow-up prompt chips tailored to
// current and recent conversation history.
func (a *Assistant) GenerateTips(ctx context.Context, current Draft, history []ChatMessage, webSearch bool) ([]Tip, error) {
	current = current.Clean()
	if !current.HasContent() && len(history) == 0 {
		return ContextualTips(current, webSearch && a.search != nil), nil
	}

	raw, err := a.callSchema(
		archive.WithLLMKind(ctx, "char_assist_tips"),
		tipsSystemPrompt,
		buildTipsUserPrompt(current, history, webSearch && a.search != nil),
		"character_tips",
		tipsSchema,
		tipsMaxTokens,
	)
	if err != nil {
		return nil, err
	}

	var parsed struct {
		Tips []Tip `json:"tips"`
	}
	if err := json.Unmarshal([]byte(stripCodeFence(raw)), &parsed); err != nil {
		return nil, fmt.Errorf("parse tips: %w", err)
	}

	out := sanitizeTips(parsed.Tips)
	if len(out) == 0 {
		return nil, fmt.Errorf("model returned no usable tips")
	}
	return out, nil
}

func buildTipsUserPrompt(current Draft, history []ChatMessage, hasSearch bool) string {
	var b strings.Builder
	b.WriteString("Current character card (JSON):\n")
	raw, _ := json.Marshal(current)
	b.Write(raw)
	b.WriteString("\n")

	if len(history) > 0 {
		recent := history
		if len(recent) > 4 {
			recent = recent[len(recent)-4:]
		}
		b.WriteString("\nRecent conversation:\n")
		for _, h := range recent {
			fmt.Fprintf(&b, "%s: %s\n", h.Role, h.Content)
		}
	}

	if hasSearch {
		b.WriteString("\nWeb search is enabled (you may suggest searching for recent lore, quotes, or memes if relevant).")
	} else {
		b.WriteString("\nWeb search is off (do not suggest searching the web).")
	}
	b.WriteString("\nSuggest 3-4 specific next edits or prompts.")
	return b.String()
}

func sanitizeTips(in []Tip) []Tip {
	var out []Tip
	seen := make(map[string]bool)
	for _, t := range in {
		if len(out) >= maxTips {
			break
		}
		label := clipLabel(strings.TrimSpace(t.Label), 32)
		prompt := strings.TrimSpace(t.Prompt)
		if label == "" || prompt == "" {
			continue
		}
		key := strings.ToLower(label)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, Tip{Label: label, Prompt: prompt})
	}
	return out
}

// ContextualTips builds up to 4 instant starter or fallback tips directly from
// the current form draft without an LLM call.
func ContextualTips(draft Draft, hasSearch bool) []Tip {
	d := draft.Clean()
	if !d.HasContent() {
		tips := []Tip{
			{
				Label:  "Real person / streamer",
				Prompt: "Research and build a full character card for ",
			},
			{
				Label:  "Fictional character",
				Prompt: "Build a full character card for ",
			},
			{
				Label:  "Original archetype",
				Prompt: "Create an original Twitch character: ",
			},
			{
				Label:  "From notes / quotes",
				Prompt: "Turn these notes and quotes into a full character card:\n",
			},
		}
		if !hasSearch {
			tips[0].Prompt = "Build a full character card for "
		}
		return tips
	}

	subject := d.Name
	if subject == "" {
		subject = d.CharName
	}
	ref := "this character"
	if subject != "" {
		ref = subject
	}

	var tips []Tip
	add := func(label, prompt string) {
		if len(tips) >= maxTips {
			return
		}
		tips = append(tips, Tip{Label: label, Prompt: prompt})
	}

	var missing []string
	if d.CharName == "" {
		missing = append(missing, "character name")
	}
	if d.CharDescription == "" {
		missing = append(missing, "short blurb")
	}
	if d.Name == "" {
		missing = append(missing, "name")
	}
	if d.Personality == "" {
		missing = append(missing, "personality")
	}
	if d.Description == "" {
		missing = append(missing, "description")
	}
	if d.FirstMessage == "" {
		missing = append(missing, "first message")
	}
	if len(missing) > 0 {
		add(
			"Fill missing fields",
			fmt.Sprintf("Fill in the missing fields (%s) for %s while keeping what is already written.", strings.Join(missing, ", "), ref),
		)
	}

	if d.Description != "" && len([]rune(d.Description)) < 140 {
		add(
			"Expand description",
			fmt.Sprintf("Expand %s's description with speech habits, catchphrases, lore, and strong opinions for spoken roleplay.", ref),
		)
	}

	if hasTTSArtifacts(d) {
		add(
			"Clean examples for TTS",
			fmt.Sprintf("Rewrite %s's message examples into pure spoken TTS text with no stage directions, asterisks, brackets, or markdown.", ref),
		)
	}

	if n := len(d.MessageExamples); n < 4 {
		need := 4 - n
		if need < 2 {
			need = 2
		}
		add(
			fmt.Sprintf("Add %d examples", need),
			fmt.Sprintf("Add %d varied Twitch chat message examples for %s mixing in-domain questions and random prompts.", need, ref),
		)
	}

	if hasSearch {
		add(
			"Search recent lore",
			fmt.Sprintf("Search the web for recent lore, quotes, and memes about %s and update the card.", ref),
		)
	}

	add(
		"Add off-topic examples",
		fmt.Sprintf("Add 3 message examples where %s reacts to random Twitch chat prompts like hot takes, life advice, and roasts.", ref),
	)
	add(
		"Sharpen voice",
		fmt.Sprintf("Review %s's card and sharpen the description, personality, and examples for stronger spoken voice and catchphrases.", ref),
	)
	add(
		"Punchier TTS rhythm",
		fmt.Sprintf("Tighten %s's message examples into snappier spoken replies that sound natural when read aloud by TTS.", ref),
	)

	return tips
}

func hasTTSArtifacts(d Draft) bool {
	for _, ex := range d.MessageExamples {
		if ttsStageRe.MatchString(ex.Response) {
			return true
		}
	}
	return false
}

func clipLabel(s string, maxRunes int) string {
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	return strings.TrimSpace(string(r[:maxRunes-1])) + "…"
}
