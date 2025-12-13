package agentic

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"app/db"
	"app/pkg/llm"

	"github.com/google/uuid"
)

// Detector detects characters mentioned in prompts using LLM with guided JSON
type Detector struct {
	client *llm.Client
}

type detectionExample struct {
	Title    string
	Prompt   string
	Expected []string
	Notes    string
}

type promptAugmentor struct {
	examples []detectionExample
}

type detectionSchema struct {
	Type       string                    `json:"type"`
	Properties detectionSchemaProperties `json:"properties"`
	Required   []string                  `json:"required"`
}

type detectionSchemaProperties struct {
	Characters detectionSchemaCharacters `json:"characters"`
}

type detectionSchemaCharacters struct {
	Type  string              `json:"type"`
	Items detectionSchemaItem `json:"items"`
}

type detectionSchemaItem struct {
	Type string   `json:"type"`
	Enum []string `json:"enum"`
}

type detectionResponse struct {
	Characters []string `json:"characters"`
}

type characterDescriptor struct {
	Name        string `json:"name"`
	ShortName   string `json:"short_name,omitempty"`
	Description string `json:"description,omitempty"`
}

func newPromptAugmentor(examples []detectionExample) promptAugmentor {
	clean := make([]detectionExample, 0, len(examples))
	for _, ex := range examples {
		prompt := strings.TrimSpace(ex.Prompt)
		if prompt == "" {
			continue
		}
		clean = append(clean, detectionExample{
			Title:    strings.TrimSpace(ex.Title),
			Prompt:   prompt,
			Expected: append([]string(nil), ex.Expected...),
			Notes:    strings.TrimSpace(ex.Notes),
		})
	}
	return promptAugmentor{examples: clean}
}

func (p promptAugmentor) render() string {
	if len(p.examples) == 0 {
		return ""
	}

	var b strings.Builder
	for idx, ex := range p.examples {
		title := ex.Title
		if title == "" {
			title = "Example"
		}
		b.WriteString(fmt.Sprintf("Example %d — %s\n", idx+1, title))
		b.WriteString("Prompt:\n\"\"\"\n")
		b.WriteString(ex.Prompt)
		b.WriteString("\n\"\"\"\nExpected response:\n")

		b.WriteString(formatDetectionResponse(ex.Expected))

		if ex.Notes != "" {
			b.WriteString("\nNotes: ")
			b.WriteString(ex.Notes)
		}

		if idx < len(p.examples)-1 {
			b.WriteString("\n")
		}
	}
	return b.String()
}

var defaultDetectionExamples = []detectionExample{
	{
		Title:    "Profession-specific references map to the expert",
		Prompt:   "The narcissism doctor explains healthy boundaries",
		Expected: []string{"Dr Les Carter"},
		Notes:    "Phrases like \"narcissism doctor\" refer to Dr Les Carter's Surviving Narcissism channel, not other streamers with doctor titles.",
	},
	{
		Title:    "Streamer aliases vs. similarly named characters",
		Prompt:   "ForsenSpectate is live with a watch party",
		Expected: []string{"forsenSpectate"},
		Notes:    "Mentioning ForsenSpectate alone should not return Forsen; only include Forsen if he is explicitly referenced.",
	},
	{
		Title:    "Explicit character names beat similarly named channels",
		Prompt:   "forsen is the god gamer",
		Expected: []string{"Forsen"},
		Notes:    "When the exact name 'Forsen' appears, never return ForsenSpectate or other lookalikes.",
	},
	{
		Title:    "Channel-specific phrasing resolves the character",
		Prompt:   "The Surviving Narcissism host shares new tips",
		Expected: []string{"Dr Les Carter"},
		Notes:    "Use the character descriptions to recognize unique shows or recurring roles even when the full name is missing.",
	},
	{
		Title:    "Keyword-driven identification",
		Prompt:   "Udisen posts a new Terraria crafting guide",
		Expected: []string{"Udisen"},
		Notes:    "Terraria guide content is unique to Udisen; ignore unrelated pop culture characters.",
	},
	{
		Title:  "No characters mentioned",
		Prompt: "The weather is nice today",
		Notes:  "Return an empty list when no provided character is clearly referenced.",
	},
	{
		Title:  "Random Characters request",
		Prompt: "2 random persons talk about microphone",
		Expected: []string{
			"Dr Les Carter",
			"Forsen",
		},
		Notes: "When a prompt requests random characters, still return exact names from the catalog. " +
			"If randomness is unclear, pick the first requested number of characters from the catalog order to stay deterministic.",
	},
	{
		Title:    "CamelCase or punctuation differences still count",
		Prompt:   "Forsen and DrDisrespect duel while Jesus watches",
		Expected: []string{"Forsen", "DrDisRespect", "Jesus"},
		Notes:    "Treat aliases like DrDisrespect or dr disrespect as DrDisRespect when the context clearly points to the same person.",
	},
	{
		Title:    "Different doctors, different domains",
		Prompt:   "The narcissism doctor argues with DrDisRespect in the arena",
		Expected: []string{"Dr Les Carter", "DrDisRespect"},
		Notes:    "Therapy or Surviving Narcissism references map to Dr Les Carter even though both names contain 'Dr'.",
	},
	{
		Title:    "Do not censor explicit character names",
		Prompt:   "Retard barges into the scene again",
		Expected: []string{"Retard"},
		Notes:    "If the catalog contains edgy names, still return them when explicitly mentioned.",
	},
}

// NewDetector creates a new character detector
func NewDetector(client *llm.Client) *Detector {
	return &Detector{
		client: client,
	}
}

// DetectCharacters analyzes a prompt and returns detected characters
func (d *Detector) DetectCharacters(ctx context.Context, prompt string, characters []db.CharacterBasicInfo) ([]db.CharacterBasicInfo, error) {
	if len(characters) == 0 {
		return nil, nil
	}

	// Build lookup maps: name -> CharacterBasicInfo
	nameToChar := make(map[string]db.CharacterBasicInfo)
	var validNames []string

	for _, char := range characters {
		if char.Name != "" {
			normalizedName := strings.ToLower(strings.TrimSpace(char.Name))
			nameToChar[normalizedName] = char
			validNames = append(validNames, char.Name)
		}
	}

	if len(validNames) == 0 {
		return nil, nil
	}

	schemaBytes, err := buildDetectionSchema(validNames)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal schema: %w", err)
	}

	// Construct system prompt
	systemPrompt := strings.Join([]string{
		"You are a careful character detection assistant.",
		"Use the provided character catalog and rules to decide which characters are clearly referenced.",
		"Descriptions, short names, and show titles often imply the character even when the exact name is missing—make thoughtful inferences when the clue uniquely fits one entry.",
		"The Surviving Narcissism therapist (\"narcissism doctor\") is Dr Les Carter, while DrDisRespect is a gaming streamer; never swap them.",
		"Return only names from the catalog and prefer returning nothing when unsure.",
	}, " ")

	// Construct user prompt with character list
	charCatalog := buildCharacterCatalog(characters)
	detectionRules := detectionRules()
	exampleSection := newPromptAugmentor(defaultDetectionExamples).render()

	var userPromptBuilder strings.Builder
	userPromptBuilder.WriteString("Character catalog (JSON):\n")
	userPromptBuilder.WriteString(charCatalog)
	userPromptBuilder.WriteString("\n\nDetection rules:\n")
	userPromptBuilder.WriteString(detectionRules)
	if exampleSection != "" {
		userPromptBuilder.WriteString("\n\nReference examples:\n")
		userPromptBuilder.WriteString(exampleSection)
	}
	userPromptBuilder.WriteString("\n\nPrompt to analyze: ")
	userPromptBuilder.WriteString(prompt)
	userPromptBuilder.WriteString("\n\nWhich characters are mentioned in this prompt? Respond with only the exact names from the available list.")
	userPrompt := userPromptBuilder.String()

	messages := []llm.Message{
		{
			Role: "system",
			Content: []llm.MessageContent{
				{Type: "text", Text: systemPrompt},
			},
		},
		{
			Role: "user",
			Content: []llm.MessageContent{
				{Type: "text", Text: userPrompt},
			},
		},
	}

	// Call LLM with guided JSON
	response, err := d.client.AskGuided(ctx, messages, schemaBytes, 0.0)
	if err != nil {
		return nil, fmt.Errorf("failed to call LLM: %w", err)
	}

	// Parse response
	var result detectionResponse
	if err := json.Unmarshal([]byte(response), &result); err != nil {
		return nil, fmt.Errorf("failed to parse LLM response: %w", err)
	}

	// Map names back to CharacterBasicInfo (deduplicate by ID)
	seenIDs := make(map[uuid.UUID]bool)
	var detected []db.CharacterBasicInfo

	for _, name := range result.Characters {
		normalized := strings.ToLower(strings.TrimSpace(name))
		if char, ok := nameToChar[normalized]; ok {
			if !seenIDs[char.ID] {
				seenIDs[char.ID] = true
				detected = append(detected, char)
			}
		}
	}

	return detected, nil
}

func buildDetectionSchema(validNames []string) ([]byte, error) {
	schema := detectionSchema{
		Type: "object",
		Properties: detectionSchemaProperties{
			Characters: detectionSchemaCharacters{
				Type: "array",
				Items: detectionSchemaItem{
					Type: "string",
					Enum: append([]string(nil), validNames...),
				},
			},
		},
		Required: []string{"characters"},
	}
	return json.Marshal(schema)
}

func formatDetectionResponse(expected []string) string {
	respJSON, err := json.MarshalIndent(detectionResponse{Characters: expected}, "", "  ")
	if err != nil {
		respJSON = []byte(`{"characters":[]}`)
	}
	return string(respJSON)
}

func buildCharacterCatalog(characters []db.CharacterBasicInfo) string {
	descriptors := make([]characterDescriptor, 0, len(characters))
	for _, char := range characters {
		desc := characterDescriptor{
			Name:        strings.TrimSpace(char.Name),
			ShortName:   strings.TrimSpace(char.ShortName),
			Description: strings.TrimSpace(char.Description),
		}
		if desc.Name == "" {
			continue
		}
		if desc.ShortName == "" {
			desc.ShortName = ""
		}
		if desc.Description == "" {
			desc.Description = ""
		}

		descriptors = append(descriptors, desc)
	}
	if len(descriptors) == 0 {
		return "[]"
	}

	data, err := json.MarshalIndent(descriptors, "", "  ")
	if err != nil {
		return "[]"
	}
	return string(data)
}

func detectionRules() string {
	rules := []string{
		"1. Only return a character when the prompt explicitly names them, uses their short name, or clearly describes their unique traits from the catalog.",
		"2. Substring overlaps or vague titles are insufficient. Do not conflate different characters that share partial names (e.g., Forsen vs. ForsenSpectate).",
		"3. If the prompt clearly describes a unique role, show, quote, or expertise from the catalog, include that character even when their name is absent. Prefer the character whose description best matches the clue.",
		"4. When the prompt provides no strong evidence for any character, respond with an empty list.",
		"5. Each character can appear at most once. Never invent names outside the catalog.",
		"6. Dr. Les Carter is NOT DrDisrespect and Dr. Disrespect is NOT Dr. Les Carter.",
		"7. When a prompt asks for N random characters (digits or words), output exactly N distinct names taken from the catalog. If true randomness is unclear, deterministically choose the first N names from the catalog list. Never return fewer names than requested; if the catalog is smaller than N, return all available names.",
		"8. Exact names always win over similarly named entries. If the user writes 'Forsen', you must return 'Forsen', not 'forsenSpectate'.",
		"9. Short names or CamelCase variations in the catalog count as valid references—map them back to the canonical entry listed.",
		"10. Do not censor or omit catalog names even if they look offensive; faithfully return them when the prompt does.",
	}
	return strings.Join(rules, "\n")
}
