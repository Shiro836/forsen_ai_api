//go:build integration

package charassist_test

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"

	"app/cfg"
	"app/pkg/charassist"
	"app/pkg/llm"
	"app/pkg/websearch"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func loadAssistant(t *testing.T) *charassist.Assistant {
	t.Helper()
	path := os.Getenv("CFG_PATH")
	if path == "" {
		path = "../../cfg/cfg.yaml"
	}
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var c cfg.Config
	require.NoError(t, yaml.Unmarshal(raw, &c))
	require.NotEmpty(t, c.LLM2.URL, "llm2.url must be configured")

	client := llm.New(&http.Client{Timeout: 2 * time.Minute}, &c.LLM2)
	var searcher charassist.Searcher
	if c.Clanker.LangSearchAPIKey != "" {
		searcher = websearch.New(c.Clanker.LangSearchAPIKey, nil)
	}
	return charassist.New(client, searcher)
}

func TestLiveChatWithSearch(t *testing.T) {
	a := loadAssistant(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	res, err := a.Chat(ctx, nil, "search who won Dota 2 TI 2025 and build a cocky character based on their coach", charassist.Draft{}, true)
	require.NoError(t, err)
	require.NotEmpty(t, res.Reply)
	require.NotEmpty(t, res.Draft.Name)
	require.NotEmpty(t, res.Draft.Description)
	t.Logf("actions=%+v reply=%q name=%q examples=%d", res.Actions, res.Reply, res.Draft.Name, len(res.Draft.MessageExamples))
}

func TestLiveGenerateAndExamples(t *testing.T) {
	a := loadAssistant(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	draft, err := a.Generate(ctx, "grumpy medieval blacksmith who hates elves and modern swords", charassist.Draft{})
	require.NoError(t, err)
	require.NotEmpty(t, draft.CharName)
	require.NotEmpty(t, draft.Description)
	require.NotEmpty(t, draft.Personality)
	require.NotEmpty(t, draft.FirstMessage)
	require.GreaterOrEqual(t, len(draft.MessageExamples), 3)
	t.Logf("generated: %s — %s (%d examples)", draft.CharName, draft.CharDescription, len(draft.MessageExamples))

	more, err := a.GenerateExamples(ctx, *draft, 3)
	require.NoError(t, err)
	require.NotEmpty(t, more)
	t.Logf("added %d examples: first=%q -> %q", len(more), more[0].Request, more[0].Response)
}
