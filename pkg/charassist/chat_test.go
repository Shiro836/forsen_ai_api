package charassist

import (
	"context"
	"testing"

	"app/db"
	"app/pkg/llm"
	"app/pkg/websearch"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type scriptedChatClient struct {
	model   string
	replies []llm.ChatResponseMessage
	reqs    []*llm.ChatRequest
}

func (s *scriptedChatClient) ReqChat(_ context.Context, req *llm.ChatRequest) (*llm.ChatResponse, error) {
	s.reqs = append(s.reqs, req)
	idx := len(s.reqs) - 1
	msg := s.replies[idx]
	return &llm.ChatResponse{
		Model:   s.model,
		Choices: []llm.ChatResponseChoice{{Message: msg, FinishReason: "stop"}},
	}, nil
}

func (s *scriptedChatClient) GetModel() string { return s.model }

type stubSearcher struct {
	results []websearch.Result
	queries []string
}

func (s *stubSearcher) Search(_ context.Context, query string, _ int) ([]websearch.Result, error) {
	s.queries = append(s.queries, query)
	return s.results, nil
}

func TestChat_SearchAndUpdateTools(t *testing.T) {
	client := &scriptedChatClient{
		model: "qwen36-hauhau",
		replies: []llm.ChatResponseMessage{
			{
				ToolCalls: []llm.ToolCall{{
					ID:   "call-1",
					Type: "function",
					Function: llm.ToolCallFunction{
						Name:      "web_search",
						Arguments: `{"query":"TI 2025 Dota winner"}`,
					},
				}},
			},
			{
				ToolCalls: []llm.ToolCall{
					{
						ID:   "call-2",
						Type: "function",
						Function: llm.ToolCallFunction{
							Name:      "update_fields",
							Arguments: `{"char_name":"Falcons Coach","description":"Coaches TI 2025 winners Team Falcons."}`,
						},
					},
					{
						ID:   "call-3",
						Type: "function",
						Function: llm.ToolCallFunction{
							Name:      "add_message_examples",
							Arguments: `{"examples":[{"request":"How did you win?","response":"Clean drafts and zero throws."}]}`,
						},
					},
				},
			},
			{
				Content: "Looked up TI 2025 and updated the card and added an example.\n[[tip: Roast drafts | Add 2 examples roasting viewer Dota drafts.]]\n[[tip: Shorter replies | Tighten examples to 1-2 punchy sentences.]]",
			},
		},
	}
	searcher := &stubSearcher{
		results: []websearch.Result{{Name: "TI 2025", URL: "https://example.com", Snippet: "Team Falcons won TI 2025."}},
	}

	a := New(client, searcher)
	res, err := a.Chat(context.Background(), nil, "make a coach for the TI 2025 winner", Draft{
		Personality: "cocky",
		MessageExamples: []db.MessageExample{
			{Request: "Old Q", Response: "Old A"},
		},
	}, true)
	require.NoError(t, err)

	assert.Equal(t, []string{"TI 2025 Dota winner"}, searcher.queries)
	assert.Equal(t, "Falcons Coach", res.Draft.CharName)
	assert.Equal(t, "Coaches TI 2025 winners Team Falcons.", res.Draft.Description)
	assert.Equal(t, "cocky", res.Draft.Personality, "untouched field stays intact")
	require.Len(t, res.Draft.MessageExamples, 2)
	assert.Equal(t, "How did you win?", res.Draft.MessageExamples[1].Request)

	assert.True(t, res.ChangedFields["char_name"])
	assert.True(t, res.ChangedFields["description"])
	assert.True(t, res.ChangedFields["message_examples"])
	assert.False(t, res.ChangedFields["personality"])

	require.Len(t, res.Actions, 3)
	assert.Equal(t, "search", res.Actions[0].Kind)
	assert.Equal(t, "edit", res.Actions[1].Kind)
	assert.Equal(t, "examples", res.Actions[2].Kind)
	assert.Contains(t, res.Reply, "TI 2025")
	assert.NotContains(t, res.Reply, "[[tip:", "tip directives must be stripped from visible reply")
	require.Len(t, res.Tips, 2)
	assert.Equal(t, "Roast drafts", res.Tips[0].Label)

	require.Len(t, client.reqs, 3)
	assert.Len(t, client.reqs[0].Tools, 5, "web_search included when searcher is configured")
	assert.Equal(t, false, client.reqs[0].ChatTemplateKwargs["enable_thinking"])
}

func TestChat_WithoutSearcherOrToggleOffOmitsWebSearchTool(t *testing.T) {
	client := &scriptedChatClient{
		model:   "qwen36-hauhau",
		replies: []llm.ChatResponseMessage{{Content: "Just answering your question."}},
	}
	a := New(client, nil)
	res, err := a.Chat(context.Background(), nil, "what traits fit a pirate?", Draft{}, true)
	require.NoError(t, err)
	assert.Empty(t, res.ChangedFields)
	assert.Len(t, client.reqs[0].Tools, 4)

	client2 := &scriptedChatClient{
		model:   "qwen36-hauhau",
		replies: []llm.ChatResponseMessage{{Content: "Answered without web search."}},
	}
	aWithSearch := New(client2, &stubSearcher{})
	_, err = aWithSearch.Chat(context.Background(), nil, "what traits fit a pirate?", Draft{}, false)
	require.NoError(t, err)
	assert.Len(t, client2.reqs[0].Tools, 4, "webSearch=false must omit web_search tool even when searcher is configured")
	assert.Contains(t, client2.reqs[0].Messages[0].StrContent, "message_examples: THE MOST IMPORTANT FIELD")
}

func TestChat_RemoveExample(t *testing.T) {
	client := &scriptedChatClient{
		model: "qwen36-hauhau",
		replies: []llm.ChatResponseMessage{
			{
				ToolCalls: []llm.ToolCall{{
					ID:   "call-1",
					Type: "function",
					Function: llm.ToolCallFunction{
						Name:      "remove_message_example",
						Arguments: `{"index":0}`,
					},
				}},
			},
			{Content: "Removed the first example."},
		},
	}
	a := New(client, nil)
	res, err := a.Chat(context.Background(), nil, "drop the first example", Draft{
		MessageExamples: []db.MessageExample{
			{Request: "Q1", Response: "A1"},
			{Request: "Q2", Response: "A2"},
		},
	}, false)
	require.NoError(t, err)
	require.Len(t, res.Draft.MessageExamples, 1)
	assert.Equal(t, "Q2", res.Draft.MessageExamples[0].Request)
	assert.True(t, res.ChangedFields["message_examples"])
}
