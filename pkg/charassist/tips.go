package charassist

import (
	"fmt"
	"regexp"
	"strings"
)

const maxTips = 4

// Tip is a clickable quick-prompt chip that prefills the chat composer.
type Tip struct {
	Label  string
	Prompt string
}

var (
	tipLineRe  = regexp.MustCompile(`^\s*(?:[-*]\s*)?\[\[tip:\s*([^|\]]+?)\s*\|\s*([^\]]+?)\s*\]\]\s*$`)
	ttsStageRe = regexp.MustCompile(`\*[^*\n]{1,40}\*|\[[^\]\n]{1,30}\]|\([^)\n]{1,25}\)|` + "```")
)

// ContextualTips builds up to 4 starter or follow-up tips directly from the
// current form draft (blank, partial, or populated) without an LLM call.
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

// ParseTips extracts trailing [[tip: Label | Prompt]] lines from an assistant
// reply and returns the cleaned reply text alongside the parsed tips.
func ParseTips(raw string) (string, []Tip) {
	lines := strings.Split(raw, "\n")
	kept := make([]string, 0, len(lines))
	var tips []Tip
	seen := make(map[string]bool)

	for _, line := range lines {
		m := tipLineRe.FindStringSubmatch(line)
		if m == nil {
			kept = append(kept, line)
			continue
		}
		if len(tips) >= maxTips {
			continue
		}
		label := clipLabel(strings.TrimSpace(m[1]), 32)
		prompt := strings.TrimSpace(m[2])
		if label == "" || prompt == "" {
			continue
		}
		key := strings.ToLower(label)
		if seen[key] {
			continue
		}
		seen[key] = true
		tips = append(tips, Tip{Label: label, Prompt: prompt})
	}

	clean := strings.TrimSpace(strings.Join(kept, "\n"))
	return clean, tips
}

func clipLabel(s string, maxRunes int) string {
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	return strings.TrimSpace(string(r[:maxRunes-1])) + "…"
}
