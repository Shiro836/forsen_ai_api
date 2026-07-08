package llmfilter

import (
	"context"
	"errors"
	"strings"
	"testing"

	"app/pkg/llm"
	"app/pkg/textfilter"
)

// fakeClient returns canned outputs in sequence, one per Ask call, recording the
// messages it received.
type fakeClient struct {
	outputs []string
	err     error
	calls   int
	lastMsg []llm.Message
}

func (c *fakeClient) Ask(_ context.Context, messages []llm.Message, _ float64) (string, error) {
	c.lastMsg = messages
	if c.err != nil {
		return "", c.err
	}
	out := c.outputs[min(c.calls, len(c.outputs)-1)]
	c.calls++
	return out, nil
}

func slice(t *testing.T, text string, s textfilter.Span) string {
	t.Helper()
	r := []rune(text)
	if s.Start < 0 || s.End > len(r) || s.Start > s.End {
		t.Fatalf("span %v out of bounds for %q (len %d)", s, text, len(r))
	}
	return string(r[s.Start:s.End])
}

func TestSpans(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		output   string
		wantText []string
	}{
		{
			name:     "no input means no call",
			input:    "   ",
			output:   "unused",
			wantText: nil,
		},
		{
			name:     "no hate yields no spans",
			input:    "I love my dog and I hate pepper",
			output:   "I love my dog and I hate pepper",
			wantText: nil,
		},
		{
			name:     "spec example tags only hateful hate",
			input:    "I love jews, I hate jews, I like black people, I hate black people, I hate pepper",
			output:   "I love jews, I <f>hate</f> jews, I like black people, I <f>hate</f> black people, I hate pepper",
			wantText: []string{"hate", "hate"},
		},
		{
			name:     "duplicate word disambiguated by position",
			input:    "I hate pepper but I hate jews",
			output:   "I hate pepper but I <f>hate</f> jews",
			wantText: []string{"hate"},
		},
		{
			name:     "wraps a whole phrase",
			input:    "kill all of them now",
			output:   "<f>kill all of them</f> now",
			wantText: []string{"kill all of them"},
		},
		{
			name:     "non-ascii offsets",
			input:    "café crème, I hate arabs ok",
			output:   "café crème, I <f>hate arabs</f> ok",
			wantText: []string{"hate arabs"},
		},
		{
			name:     "unclosed tag extends to end",
			input:    "go away nerd",
			output:   "go away <f>nerd",
			wantText: []string{"nerd"},
		},
		{
			name:     "stray closing tag ignored",
			input:    "hello world",
			output:   "hello</f> world",
			wantText: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := &fakeClient{outputs: []string{tc.output}}

			spans, err := New(c).Spans(context.Background(), tc.input, "")
			if err != nil {
				t.Fatalf("Spans: %v", err)
			}

			if len(spans) != len(tc.wantText) {
				t.Fatalf("got %d spans %v, want %d", len(spans), spans, len(tc.wantText))
			}
			for i, s := range spans {
				if got := slice(t, tc.input, s); got != tc.wantText[i] {
					t.Errorf("span %d = %q, want %q", i, got, tc.wantText[i])
				}
			}
		})
	}
}

func TestSpansNoCallOnEmpty(t *testing.T) {
	c := &fakeClient{outputs: []string{"x"}}
	if _, err := New(c).Spans(context.Background(), "  \n\t ", ""); err != nil {
		t.Fatalf("Spans: %v", err)
	}
	if c.calls != 0 {
		t.Fatalf("expected no LLM call, got %d", c.calls)
	}
}

func TestCustomPromptReachesSystemMessage(t *testing.T) {
	const custom = "filter everything related to making illegal items"
	c := &fakeClient{outputs: []string{"hello world"}}

	if _, err := New(c).Spans(context.Background(), "hello world", custom); err != nil {
		t.Fatalf("Spans: %v", err)
	}

	system := c.lastMsg[0].Content[0].Text
	if !strings.Contains(system, custom) {
		t.Fatalf("system prompt does not contain the custom rules, got %q", system)
	}
	if !strings.HasPrefix(system, systemPrompt) {
		t.Fatal("custom rules must be appended after the base prompt, not replace it")
	}
}

func TestEmptyCustomPromptLeavesSystemMessageUnchanged(t *testing.T) {
	c := &fakeClient{outputs: []string{"hello world"}}

	if _, err := New(c).Spans(context.Background(), "hello world", "  \n "); err != nil {
		t.Fatalf("Spans: %v", err)
	}

	if got := c.lastMsg[0].Content[0].Text; got != systemPrompt {
		t.Fatalf("blank custom rules must leave the base prompt untouched, got %q", got)
	}
}

func TestReplySpansMapsToReplyAndSendsContext(t *testing.T) {
	prompt := "what do you think about gypsies?"
	reply := "I hate them"
	c := &fakeClient{outputs: []string{"I <f>hate</f> them"}}

	spans, err := New(c).ReplySpans(context.Background(), prompt, reply, "")
	if err != nil {
		t.Fatalf("ReplySpans: %v", err)
	}
	if len(spans) != 1 || slice(t, reply, spans[0]) != "hate" {
		t.Fatalf("got spans %v, want one over %q", spans, "hate")
	}

	user := c.lastMsg[1].Content[0].Text
	if !strings.Contains(user, prompt) || !strings.Contains(user, reply) {
		t.Fatalf("reply mode must send prompt as context and the reply as target, got %q", user)
	}
}

func TestReplySpansVerbatimCheckIsReplyOnly(t *testing.T) {
	// the model echoes the context too; stripping must not match the reply, so
	// it retries, and the corrected output maps over the reply alone.
	c := &fakeClient{outputs: []string{
		"a viewer asked: what about jews? I <f>hate</f> them",
		"I <f>hate</f> them",
	}}

	spans, err := New(c).ReplySpans(context.Background(), "what about jews?", "I hate them", "")
	if err != nil {
		t.Fatalf("ReplySpans: %v", err)
	}
	if c.calls != 2 {
		t.Fatalf("expected 2 calls, got %d", c.calls)
	}
	if len(spans) != 1 || slice(t, "I hate them", spans[0]) != "hate" {
		t.Fatalf("got spans %v, want one over %q", spans, "hate")
	}
}

func TestSpansRetriesOnDrift(t *testing.T) {
	input := "I hate jews"
	c := &fakeClient{outputs: []string{
		"I really <f>hate</f> jews",
		"I <f>hate</f> jews",
	}}

	spans, err := New(c).Spans(context.Background(), input, "")
	if err != nil {
		t.Fatalf("Spans: %v", err)
	}
	if c.calls != 2 {
		t.Fatalf("expected 2 calls, got %d", c.calls)
	}
	if len(spans) != 1 || slice(t, input, spans[0]) != "hate" {
		t.Fatalf("got spans %v, want one over %q", spans, "hate")
	}

	roles := make([]string, len(c.lastMsg))
	for i, m := range c.lastMsg {
		roles[i] = m.Role
	}
	want := []string{"system", "user", "assistant", "user"}
	if len(roles) != len(want) {
		t.Fatalf("retry sent roles %v, want %v", roles, want)
	}
	for i := range want {
		if roles[i] != want[i] {
			t.Fatalf("retry sent roles %v, want %v", roles, want)
		}
	}
	if c.lastMsg[2].Content[0].Text != "I really <f>hate</f> jews" {
		t.Fatalf("retry did not include the failed attempt, got %q", c.lastMsg[2].Content[0].Text)
	}
}

func TestSpansErrorsWhenNeverVerbatim(t *testing.T) {
	c := &fakeClient{outputs: []string{"totally different text"}}
	_, err := New(c).Spans(context.Background(), "I hate jews", "")
	if err == nil {
		t.Fatal("expected error when model never reproduces input")
	}
	if c.calls != maxAttempts {
		t.Fatalf("expected %d attempts, got %d", maxAttempts, c.calls)
	}
}

func TestSpansPropagatesClientError(t *testing.T) {
	c := &fakeClient{err: errors.New("boom")}
	if _, err := New(c).Spans(context.Background(), "x", ""); err == nil {
		t.Fatal("expected client error to propagate")
	}
}
