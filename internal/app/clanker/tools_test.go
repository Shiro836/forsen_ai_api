package clanker

import (
	"testing"
)

func TestParseToolCallsFromContent(t *testing.T) {
	tests := []struct {
		name          string
		content       string
		wantCount     int
		wantNames     []string
		wantArgs      []string
		wantIDs       []string
		wantClean     string
		wantNilCalls  bool
	}{
		{
			name:    "exact model output from real usage",
			content: `<tool_calls>[{"name": "web_search", "arguments": {"query": "latest Iran news September 2025"}}]</tool_calls>`,
			wantCount: 1,
			wantNames: []string{"web_search"},
			wantArgs:  []string{`{"query": "latest Iran news September 2025"}`},
			wantIDs:   []string{"call_0"},
		},
		{
			name:    "with thinking prefix before tool calls",
			content: "Here are my reasoning steps:\nThe user wants recent news about Iran. I should search the web.\n[BEGIN FINAL RESPONSE]\n<tool_calls>[{\"name\": \"web_search\", \"arguments\": {\"query\": \"Iran news 2025\"}}]</tool_calls>",
			wantCount: 1,
			wantNames: []string{"web_search"},
			wantArgs:  []string{`{"query": "Iran news 2025"}`},
			wantIDs:   []string{"call_0"},
		},
		{
			name:    "with model-provided id",
			content: `<tool_calls>[{"name": "web_search", "arguments": {"query": "test"}, "id": "abc123"}]</tool_calls>`,
			wantCount: 1,
			wantNames: []string{"web_search"},
			wantArgs:  []string{`{"query": "test"}`},
			wantIDs:   []string{"abc123"},
		},
		{
			name:    "multiple tool calls",
			content: `<tool_calls>[{"name": "web_search", "arguments": {"query": "first"}}, {"name": "web_search", "arguments": {"query": "second"}}]</tool_calls>`,
			wantCount: 2,
			wantNames: []string{"web_search", "web_search"},
			wantArgs:  []string{`{"query": "first"}`, `{"query": "second"}`},
			wantIDs:   []string{"call_0", "call_1"},
		},
		{
			name:         "no tool calls - plain text",
			content:      "Hello, how can I help you today?",
			wantNilCalls: true,
			wantClean:    "Hello, how can I help you today?",
		},
		{
			name:         "empty tool_calls tags",
			content:      "<tool_calls></tool_calls>",
			wantNilCalls: true,
			wantClean:    "<tool_calls></tool_calls>",
		},
		{
			name:         "malformed JSON inside tags",
			content:      `<tool_calls>not valid json</tool_calls>`,
			wantNilCalls: true,
			wantClean:    `<tool_calls>not valid json</tool_calls>`,
		},
		{
			name:         "only opening tag",
			content:      `<tool_calls>[{"name": "web_search"}]`,
			wantNilCalls: true,
			wantClean:    `<tool_calls>[{"name": "web_search"}]`,
		},
		{
			name:         "empty array",
			content:      `<tool_calls>[]</tool_calls>`,
			wantNilCalls: true,
			wantClean:    `<tool_calls>[]</tool_calls>`,
		},
		{
			name:    "text before and after tool calls",
			content: "Let me search for that.\n<tool_calls>[{\"name\": \"web_search\", \"arguments\": {\"query\": \"test\"}}]</tool_calls>\nSome trailing text",
			wantCount: 1,
			wantNames: []string{"web_search"},
			wantArgs:  []string{`{"query": "test"}`},
			wantIDs:   []string{"call_0"},
			wantClean: "Let me search for that.",
		},
		{
			name:    "whitespace around JSON inside tags",
			content: "<tool_calls>\n  [{\"name\": \"web_search\", \"arguments\": {\"query\": \"test\"}}]\n</tool_calls>",
			wantCount: 1,
			wantNames: []string{"web_search"},
			wantArgs:  []string{`{"query": "test"}`},
			wantIDs:   []string{"call_0"},
		},
		{
			name:    "complex arguments",
			content: `<tool_calls>[{"name": "web_search", "arguments": {"query": "test with \"quotes\" and special chars <>&"}}]</tool_calls>`,
			wantCount: 1,
			wantNames: []string{"web_search"},
			wantArgs:  []string{`{"query": "test with \"quotes\" and special chars <>&"}`},
			wantIDs:   []string{"call_0"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls, clean := parseToolCallsFromContent(tt.content)

			if tt.wantNilCalls {
				if calls != nil {
					t.Errorf("expected nil calls, got %d calls", len(calls))
				}
				if clean != tt.wantClean {
					t.Errorf("clean content mismatch:\n  got:  %q\n  want: %q", clean, tt.wantClean)
				}
				return
			}

			if len(calls) != tt.wantCount {
				t.Fatalf("expected %d calls, got %d", tt.wantCount, len(calls))
			}

			for i, call := range calls {
				if call.Function.Name != tt.wantNames[i] {
					t.Errorf("call[%d] name: got %q, want %q", i, call.Function.Name, tt.wantNames[i])
				}
				if call.Function.Arguments != tt.wantArgs[i] {
					t.Errorf("call[%d] args: got %q, want %q", i, call.Function.Arguments, tt.wantArgs[i])
				}
				if call.ID != tt.wantIDs[i] {
					t.Errorf("call[%d] id: got %q, want %q", i, call.ID, tt.wantIDs[i])
				}
				if call.Type != "function" {
					t.Errorf("call[%d] type: got %q, want %q", i, call.Type, "function")
				}
			}

			if tt.wantClean != "" && clean != tt.wantClean {
				t.Errorf("clean content mismatch:\n  got:  %q\n  want: %q", clean, tt.wantClean)
			}
		})
	}
}
