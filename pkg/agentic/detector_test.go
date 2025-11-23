package agentic_test

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"testing"
	"time"

	"app/cfg"
	"app/db"
	"app/pkg/agentic"
	"app/pkg/llm"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

var testCfg *cfg.Config

func TestMain(m *testing.M) {
	var cfgPath string
	flag.StringVar(&cfgPath, "cfg-path", "../../cfg/cfg.yaml", "path to config file")
	flag.Parse()

	cfgFile, err := os.ReadFile(cfgPath)
	if err != nil {
		log.Fatalf("can't open %s file: %v", cfgPath, err)
	}
	if err = yaml.Unmarshal(cfgFile, &testCfg); err != nil {
		log.Fatal("can't unmarshal cfg.yaml file", err)
	}

	os.Exit(m.Run())
}

func TestDetectCharacters(t *testing.T) {
	const (
		nameForsen  = "Forsen"
		nameDrLes   = "Dr Les Carter"
		nameBane    = "Bane"
		nameVadikus = "Vadikus"
		nameRetard  = "Retard"
		nameAlice   = "Alice"
		nameBob     = "Bob"
	)

	// Create test characters
	forsenID := uuid.New()
	drLesID := uuid.New()
	baneID := uuid.New()
	vadikusID := uuid.New()
	retardID := uuid.New()

	allCharacters := []db.CharacterBasicInfo{
		{ID: drLesID, Name: nameDrLes},
		{ID: forsenID, Name: nameForsen},
		{ID: baneID, Name: nameBane},
		{ID: vadikusID, Name: nameVadikus},
		{ID: retardID, Name: nameRetard},
	}

	// Create lookup map for expected characters
	nameToID := map[string]uuid.UUID{
		nameDrLes:   drLesID,
		nameForsen:  forsenID,
		nameBane:    baneID,
		nameVadikus: vadikusID,
		nameRetard:  retardID,
	}

	testCases := []struct {
		name          string
		characters    []db.CharacterBasicInfo
		prompt        string
		expectedNames []string
		expectedLen   int
		shouldBeEmpty bool
	}{
		{
			name:          "detect forsen and dr les carter",
			characters:    allCharacters,
			prompt:        "narcissism doctor tries to heal forsen, but god gamer refuses to listen",
			expectedNames: []string{nameForsen, nameDrLes},
		},
		{
			name:          "empty character list",
			characters:    []db.CharacterBasicInfo{},
			prompt:        "some prompt",
			shouldBeEmpty: true,
		},
		{
			name: "no character mentions",
			characters: []db.CharacterBasicInfo{
				{ID: uuid.New(), Name: nameAlice},
				{ID: uuid.New(), Name: nameBob},
			},
			prompt:        "The weather is nice today",
			shouldBeEmpty: true,
		},
		{
			name:          "detect bane",
			characters:    allCharacters,
			prompt:        "Bane is a big guy",
			expectedNames: []string{nameBane},
		},
		{
			name:          "detect vadikus",
			characters:    allCharacters,
			prompt:        "Vadikus is streaming",
			expectedNames: []string{nameVadikus},
		},
		{
			name:          "multiple mentions same character",
			characters:    allCharacters,
			prompt:        "Forsen and forsen and FORSEN are all the same god gamer",
			expectedNames: []string{nameForsen},
		},
		{
			name:          "all characters mentioned",
			characters:    allCharacters,
			prompt:        "Dr Les Carter talks to Forsen while Bane watches and Vadikus streams, Retard joins",
			expectedNames: []string{nameDrLes, nameForsen, nameBane, nameVadikus, nameRetard},
		},
		{
			name:          "ambiguous similar names",
			characters:    allCharacters,
			prompt:        "The narcissism doctor is here",
			expectedNames: []string{nameDrLes}, // Should detect Dr Les Carter from context
		},
		{
			name: "long character list",
			characters: []db.CharacterBasicInfo{
				{ID: uuid.New(), Name: "John"},
				{ID: uuid.New(), Name: "Mike"},
				{ID: uuid.New(), Name: "Sarah"},
				{ID: uuid.New(), Name: "David"},
				{ID: uuid.New(), Name: "Emma"},
				{ID: uuid.New(), Name: "James"},
				{ID: uuid.New(), Name: "Olivia"},
				{ID: uuid.New(), Name: "Robert"},
				{ID: uuid.New(), Name: "Sophia"},
				{ID: uuid.New(), Name: "William"},
				{ID: forsenID, Name: nameForsen},
			},
			prompt:        "Forsen is among many",
			expectedNames: []string{nameForsen},
		},
		{
			name:          "case insensitive matching",
			characters:    allCharacters,
			prompt:        "BANE and forsen are fighting",
			expectedNames: []string{nameBane, nameForsen},
		},
		{
			name:        "explicit random character count request of 3",
			characters:  allCharacters,
			prompt:      "3 random characters talk about cheese",
			expectedLen: 3,
		},
		{
			name:        "explicit random character count request of 2",
			characters:  allCharacters,
			prompt:      "2 random characters talk about cheese",
			expectedLen: 2,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Create LLM client from config
			httpClient := &http.Client{Timeout: 30 * time.Second}
			client := llm.New(httpClient, &testCfg.LLM)
			detector := agentic.NewDetector(client)

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			detected, err := detector.DetectCharacters(ctx, tc.prompt, tc.characters)
			require.NoError(t, err)

			if tc.shouldBeEmpty {
				assert.Empty(t, detected, "Expected no characters to be detected")
				return
			}

			if tc.expectedLen > 0 {
				assert.Len(t, detected, tc.expectedLen, "Expected %d characters, got %d", tc.expectedLen, len(detected))
			}

			// Build detected names for logging
			var detectedNames []string
			for _, char := range detected {
				detectedNames = append(detectedNames, char.Name)
			}
			t.Logf("Detected characters: %v", detectedNames)

			if len(tc.expectedNames) > 0 {
				require.Len(t, detected, len(tc.expectedNames), "Expected %d characters, got %d: %v", len(tc.expectedNames), len(detected), detectedNames)

				detectedMap := make(map[string]db.CharacterBasicInfo)
				for _, char := range detected {
					detectedMap[char.Name] = char
				}

				for _, expectedName := range tc.expectedNames {
					detectedChar, found := detectedMap[expectedName]
					assert.True(t, found, "Expected character '%s' to be detected, got: %v", expectedName, detectedNames)

					if found {
						// Assert UUID matches the input
						expectedID, exists := nameToID[expectedName]
						if exists {
							assert.Equal(t, expectedID, detectedChar.ID, "Character '%s' has wrong UUID: expected %s, got %s", expectedName, expectedID, detectedChar.ID)
						}
					}
				}
			}
		})
	}
}
