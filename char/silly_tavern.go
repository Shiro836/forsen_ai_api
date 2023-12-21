package char

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	pngstruct "github.com/dsoprea/go-png-image-structure"
)

func FromPngSillyTavernCard(pngCard []byte) (*Card, error) {
	if charData, err := parsePngSillyTavernCard(pngCard); err != nil {
		return nil, fmt.Errorf("failed to parse silly tavern png card: %w", err)
	} else if charCard, err := sillyTavernBytesToCard(charData); err != nil {
		return nil, fmt.Errorf("failed to convert silly tavern char data to card: %w", err)
	} else {
		return charCard, nil
	}
}

func decodePngTextChunk(data []byte) (string, string, error) {
	naming := true // zero byte is a separator between key and value

	key, value := strings.Builder{}, strings.Builder{}

	arr := make([]uint8, len(data))

	reader := bytes.NewReader(data)

	if err := binary.Read(reader, binary.BigEndian, &arr); err != nil && err != io.EOF {
		return "", "", fmt.Errorf("failed to read binary data in png tEXt chunk: %w", err)
	}

	for i := range arr {
		if naming {
			if arr[i] > 0 {
				key.WriteString(string(arr[i]))
			} else {
				naming = false
			}
		} else {
			if arr[i] > 0 {
				value.WriteString(string(arr[i]))
			} else {
				return "", "", fmt.Errorf("unexpected zero byte in text chunk")
			}
		}
	}

	return key.String(), value.String(), nil
}

func parsePngSillyTavernCard(pngCard []byte) ([]byte, error) {
	mc, err := pngstruct.NewPngMediaParser().ParseBytes(pngCard)
	if err != nil {
		return nil, fmt.Errorf("failed to parse png: %w", err)
	}

	cs := mc.(*pngstruct.ChunkSlice)

	index := cs.Index()
	textChunks, found := index["tEXt"]
	if !found {
		return nil, fmt.Errorf("failed to find text")
	}

	for _, textChunk := range textChunks {
		key, text, err := decodePngTextChunk(textChunk.Data)
		if err != nil {
			continue
		}
		if key == "chara" {
			decodedVal, err := base64.StdEncoding.DecodeString(text)
			if err != nil {
				return nil, fmt.Errorf("failed to decode val in chara tEXt chunk in png file: %w", err)
			}

			return decodedVal, nil
		}
	}

	return nil, fmt.Errorf("no chara tEXt chunk in png file")

}

type extensions struct {
	DepthPrompt struct {
		Depth  int    `json:"depth"`
		Prompt string `json:"prompt"`
	} `json:"depth_prompt"`
	Chub struct {
		AltExpressions struct{} `json:"alt_expressions"`
		// Expression type `json:"expressions"` // ??
		FullPath         string `json:"full_path"`
		ID               int    `json:"id"`
		RelatedLorebooks []struct {
			ID        int    `json:"id"`
			Path      string `json:"path"`
			CommitRef string `json:"commit_ref"`
			Version   string `json:"version"`
		} `json:"related_lorebooks"`
	} `json:"chub"`
}

type sillyTavernCard struct {
	Data *sillyTavernCard `json:"data"` // sometimes it's in data and sometimes not doctorWTF

	Name         string `json:"name"`
	Description  string `json:"description"`
	Personality  string `json:"personality"`
	FirstMessage string `json:"first_mes"`
	// Avatar       string // how should it look like?
	MessageExample          string `json:"mes_example"`
	Scenario                string `json:"scenario"`
	CreatorNotes            string `json:"creator_notes"`
	SystemPrompt            string `json:"system_prompt"`
	PostHistoryInstructions string `json:"post_history_instructions"`
	//AlternateGreetings      []string `json:"alternate_greetings"`
	Tags             []string `json:"tags"`
	Creator          string   `json:"creator"`
	CharacterVersion string   `json:"character_version"`
	// Extensions              extensions `json:"extensions"`
	CharacterBook struct {
		Name              string     `json:"character_book"`
		Description       string     `json:"description"`
		ScanDepth         int        `json:"scan_depth"`
		TokenBudget       int        `json:"token_budget"`
		RecursiveScanning bool       `json:"recursive_scanning"`
		Extensions        extensions `json:"extensions"`
	} `json:"character_book"`
	Spec        string `json:"spec"`
	SpecVersion string `json:"spec_version"`
}

func sillyTavernCardToCard(sillyTavernCard *sillyTavernCard) *Card {
	for sillyTavernCard.Data != nil {
		sillyTavernCard = sillyTavernCard.Data
	}

	return &Card{
		Name:                    sillyTavernCard.Name,
		Description:             sillyTavernCard.Description,
		Personality:             sillyTavernCard.Personality,
		FirstMessage:            sillyTavernCard.FirstMessage,
		MessageExample:          sillyTavernCard.MessageExample,
		Scenario:                sillyTavernCard.Scenario,
		SystemPrompt:            sillyTavernCard.SystemPrompt,
		PostHistoryInstructions: sillyTavernCard.PostHistoryInstructions,
	}
}

func sillyTavernBytesToCard(data []byte) (*Card, error) {
	var sillyTavernCard *sillyTavernCard
	if err := json.Unmarshal(data, &sillyTavernCard); err != nil {
		return nil, fmt.Errorf("failed to unmarshal silly tavern data: %w", err)
	}

	return sillyTavernCardToCard(sillyTavernCard), nil
}
