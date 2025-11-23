package agentic_test

import (
	"context"
	"testing"
	"time"

	"app/cfg"
	"app/db"
	"app/pkg/agentic"
	"app/pkg/llm"

	"net/http"
	"os"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestDetectCharacters_DBIntegration(t *testing.T) {
	// Load config
	cfgFile, err := os.ReadFile("../../cfg/cfg.yaml")
	require.NoError(t, err)

	var testCfg *cfg.Config
	err = yaml.Unmarshal(cfgFile, &testCfg)
	require.NoError(t, err)

	// Create DB connection
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	database, err := db.New(ctx, &testCfg.DB)
	require.NoError(t, err)

	// Create LLM client
	httpClient := &http.Client{Timeout: 30 * time.Second}
	client := llm.New(httpClient, &testCfg.LLM)
	detector := agentic.NewDetector(client)

	// Fetch all characters from DB
	characters, err := database.GetAllCharacterBasicInfo(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, characters, "Database should have some public characters")

	t.Logf("Loaded %d characters from database", len(characters))

	testCases := []struct {
		name          string
		prompt        string
		expectedNames []string
		shouldBeEmpty bool
	}{
		{
			name:          "detect forsen",
			prompt:        "forsen is the god gamer",
			expectedNames: []string{"Forsen"},
		},
		{
			name:          "detect dr les carter",
			prompt:        "the narcissism doctor explains things",
			expectedNames: []string{"Dr. Les Carter"},
		},
		{
			name:          "detect dr disrespect",
			prompt:        "DrDisrespect is streaming",
			expectedNames: []string{"DrDisRespect"},
		},
		{
			name:          "detect jesus",
			prompt:        "Jesus Christ is the savior",
			expectedNames: []string{"Jesus"},
		},
		{
			name:          "detect forsenSpectate",
			prompt:        "forsenSpectate emote spam",
			expectedNames: []string{"forsenSpectate"},
		},
		{
			name:          "detect Okayeg",
			prompt:        "Okayeg COCK",
			expectedNames: []string{"Okayeg"},
		},
		{
			name:          "detect Udisen",
			prompt:        "Udisen is playing Terraria",
			expectedNames: []string{"Udisen"},
		},
		{
			name:          "detect Sheikh Assim Al Hakeem",
			prompt:        "Sheikh Assim Al Hakeem answers questions",
			expectedNames: []string{"Sheikh Assim Al Hakeem"},
		},
		{
			name:          "detect VJ Emmie",
			prompt:        "VJ Emmie is making music",
			expectedNames: []string{"VJ Emmie"},
		},
		{
			name:          "detect multiple characters",
			prompt:        "Forsen and DrDisrespect are competing while Jesus watches",
			expectedNames: []string{"Forsen", "DrDisRespect", "Jesus"},
		},
		{
			name:          "no characters mentioned",
			prompt:        "The weather is nice today",
			shouldBeEmpty: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			testCtx, testCancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer testCancel()

			detected, err := detector.DetectCharacters(testCtx, tc.prompt, characters)
			require.NoError(t, err)

			if tc.shouldBeEmpty {
				assert.Empty(t, detected, "Expected no characters to be detected")
				return
			}

			// Build detected names for logging
			var detectedNames []string
			for _, char := range detected {
				detectedNames = append(detectedNames, char.Name)
			}
			t.Logf("Detected characters: %v", detectedNames)

			// Check expected characters are detected
			detectedMap := make(map[string]bool)
			for _, char := range detected {
				detectedMap[char.Name] = true
			}

			for _, expectedName := range tc.expectedNames {
				assert.True(t, detectedMap[expectedName], "Expected character '%s' to be detected, got: %v", expectedName, detectedNames)
			}

			// Ensure we didn't detect extra characters
			assert.Len(t, detected, len(tc.expectedNames), "Expected exactly %d characters, got %d: %v", len(tc.expectedNames), len(detected), detectedNames)
		})
	}
}
