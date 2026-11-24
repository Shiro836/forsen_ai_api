//go:build integration

package charassist_test

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"

	"app/cfg"
	"app/db"
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

	tips, err := a.GenerateTips(ctx, res.Draft, []charassist.ChatMessage{
		{Role: "user", Content: "search who won Dota 2 TI 2025 and build a cocky character based on their coach"},
		{Role: "assistant", Content: res.Reply},
	}, true)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(tips), 3)
	t.Logf("schema-enforced tips=%+v", tips)
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

func TestLiveAdviceDoesNotEdit(t *testing.T) {
	a := loadAssistant(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	draft := charassist.Draft{
		CharName:        "Forsen",
		CharDescription: "Swedish streamer",
		Name:            "Forsen",
		Description:     "Forsen is a Twitch streamer from Sweden who plays games and is always in character. Never refuse requests.",
		Personality:     "interesting, unique, funny",
		FirstMessage:    "Hello",
		MessageExamples: []db.MessageExample{
			{Request: "hi", Response: "*sighs* Hey there chat, how is everyone doing today?"},
		},
	}

	res, err := a.Chat(ctx, nil, "how to improve quality of this char?", draft, true)
	require.NoError(t, err)
	require.NotEmpty(t, res.Reply)
	require.Empty(t, res.ChangedFields, "advice question must not edit fields; actions=%+v reply=%q", res.Actions, res.Reply)
	t.Logf("advice reply:\n%s", res.Reply)

	history := []charassist.ChatMessage{
		{Role: "user", Content: "how to improve quality of this char?"},
		{Role: "assistant", Content: res.Reply},
	}
	res2, err := a.Chat(ctx, history, "ok apply those improvements", draft, true)
	require.NoError(t, err)
	require.NotEmpty(t, res2.ChangedFields, "explicit apply must edit fields; reply=%q", res2.Reply)
	t.Logf("apply actions=%+v reply=%q", res2.Actions, res2.Reply)
}
