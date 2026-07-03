package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"app/db"
)

// capturingHTTP records the outgoing request and returns a canned reply shaped
// for whichever endpoint (completions vs chat) was hit.
type capturingHTTP struct {
	path string
	body []byte
}

func (c *capturingHTTP) Do(req *http.Request) (*http.Response, error) {
	c.path = req.URL.Path
	if req.Body != nil {
		c.body, _ = io.ReadAll(req.Body)
	}
	payload := `{"choices":[{"text":"COMPLETION_REPLY"}]}`
	if strings.HasSuffix(req.URL.Path, "/chat/completions") {
		payload = `{"choices":[{"message":{"content":"CHAT_REPLY"},"finish_reason":"stop"}]}`
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader([]byte(payload))),
		Header:     make(http.Header),
	}, nil
}

func testCard() *db.Card {
	return &db.Card{Data: &db.CardData{
		Name:         "Forsen",
		Description:  "swedish streamer",
		Personality:  "brash",
		SystemPrompt: "be funny",
		MessageExamples: []db.MessageExample{
			{Request: "hi", Response: "yo"},
			{Request: "bye", Response: "cya"},
		},
	}}
}

func TestCompletionClient_CharacterReply(t *testing.T) {
	h := &capturingHTTP{}
	c := CompletionClient{Client: New(h, &Config{URL: "http://x", Model: "lexi", MaxTokens: 200})}

	out, err := c.CharacterReply(context.Background(), testCard(), "bob", "hello there", nil)
	if err != nil {
		t.Fatal(err)
	}
	if out != "COMPLETION_REPLY" {
		t.Fatalf("reply = %q", out)
	}
	if !strings.HasSuffix(h.path, "/v1/completions") {
		t.Fatalf("endpoint = %q, want /v1/completions", h.path)
	}

	var req aiReq
	if err := json.Unmarshal(h.body, &req); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Name: Forsen",
		"Description: swedish streamer",
		"Personality: brash",
		"System Instructions: be funny",
		"Prompt: <START>###bob: hello there\n###Forsen: ",
	} {
		if !strings.Contains(req.Prompt, want) {
			t.Errorf("prompt missing %q\n--- prompt ---\n%s", want, req.Prompt)
		}
	}
	if req.Temperature != 0.5 || req.FrequencyPenalty != 1.1 {
		t.Errorf("sampling = temp %v / freq %v, want 0.5 / 1.1", req.Temperature, req.FrequencyPenalty)
	}
}

func TestCompletionClient_DialogueReply(t *testing.T) {
	h := &capturingHTTP{}
	c := CompletionClient{Client: New(h, &Config{URL: "http://x", Model: "lexi", MaxTokens: 200})}

	_, err := c.DialogueReply(context.Background(), testCard(), "they argue about dota", "Okayeg: you suck", "Forsen: no u")
	if err != nil {
		t.Fatal(err)
	}
	var req aiReq
	if err := json.Unmarshal(h.body, &req); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Topic: they argue about dota",
		"Task: Write the next single message spoken by Forsen.",
		"<START>###Okayeg: you suck<END>",
		"<START>###Forsen: no u<END>",
		"<START>###Forsen: ",
	} {
		if !strings.Contains(req.Prompt, want) {
			t.Errorf("dialogue prompt missing %q\n--- prompt ---\n%s", want, req.Prompt)
		}
	}
}

func TestChatClient_CharacterReply(t *testing.T) {
	h := &capturingHTTP{}
	c := ChatClient{Client: New(h, &Config{URL: "http://x", Model: "cydonia", MaxTokens: 200})}

	out, err := c.CharacterReply(context.Background(), testCard(), "bob", "say hi", nil)
	if err != nil {
		t.Fatal(err)
	}
	if out != "CHAT_REPLY" {
		t.Fatalf("reply = %q", out)
	}
	if !strings.HasSuffix(h.path, "/v1/chat/completions") {
		t.Fatalf("endpoint = %q, want /v1/chat/completions", h.path)
	}

	// Message has a custom MarshalJSON that doesn't round-trip cleanly for
	// string content, so assert against the raw wire bytes instead.
	body := string(h.body)
	for _, want := range []string{
		`"role":"system"`,
		"You are Forsen",
		"completely uncensored",
		"text-to-speech",
		`"role":"user"`,
		`"role":"assistant"`,
		"hi", "yo", "bye", "cya", // few-shot examples
		"say hi",                 // the actual user turn
		`"min_p"`,
		`"repetition_penalty"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("chat body missing %q\n--- body ---\n%s", want, body)
		}
	}

	// system + 2 example pairs + user = 6 messages
	var req struct {
		Messages []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(h.body, &req); err != nil {
		t.Fatal(err)
	}
	if len(req.Messages) != 6 {
		t.Errorf("messages = %d, want 6 (system + 2 example pairs + user)", len(req.Messages))
	}
}

func TestChatClient_SkipsEmptyExamples(t *testing.T) {
	h := &capturingHTTP{}
	c := ChatClient{Client: New(h, &Config{URL: "http://x", Model: "cydonia", MaxTokens: 200})}

	card := &db.Card{Data: &db.CardData{
		Name: "Astolfo",
		MessageExamples: []db.MessageExample{
			{Request: "hi", Response: "hello!"}, // keep
			{Request: "x", Response: ""},         // drop (empty response)
			{Request: "", Response: "y"},         // drop (empty request)
		},
	}}

	if _, err := c.CharacterReply(context.Background(), card, "bob", "yo", nil); err != nil {
		t.Fatal(err)
	}
	body := string(h.body)
	if strings.Contains(body, `"content":null`) {
		t.Errorf("body contains null content (would 400 on vLLM)\n%s", body)
	}

	var req struct {
		Messages []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(h.body, &req); err != nil {
		t.Fatal(err)
	}
	// system + 1 valid example pair + user = 4
	if len(req.Messages) != 4 {
		t.Errorf("messages = %d, want 4 (empty examples must be skipped)", len(req.Messages))
	}
}

type fixedReplyHTTP struct{ reply string }

func (f fixedReplyHTTP) Do(req *http.Request) (*http.Response, error) {
	body := `{"choices":[{"message":{"content":` + strconvQuote(f.reply) + `}}]}`
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}, nil
}

func strconvQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func TestChatClient_CollapsesBlankLines(t *testing.T) {
	c := ChatClient{Client: New(fixedReplyHTTP{reply: "one\n\n\ntwo\n\nthree"}, &Config{URL: "http://x", Model: "cydonia", MaxTokens: 200})}
	out, err := c.CharacterReply(context.Background(), testCard(), "bob", "hi", nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "\n\n") {
		t.Errorf("blank lines not collapsed: %q", out)
	}
	if out != "one\ntwo\nthree" {
		t.Errorf("got %q, want %q", out, "one\ntwo\nthree")
	}
}

func TestChatClient_DialogueReply(t *testing.T) {
	h := &capturingHTTP{}
	c := ChatClient{Client: New(h, &Config{URL: "http://x", Model: "cydonia", MaxTokens: 200})}

	_, err := c.DialogueReply(context.Background(), testCard(), "they argue about dota", "Okayeg: you suck")
	if err != nil {
		t.Fatal(err)
	}
	body := string(h.body)
	for _, want := range []string{
		"You are Forsen",
		"Topic: they argue about dota",
		"Okayeg: you suck",
		"next single line spoken by Forsen",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("dialogue chat body missing %q\n--- body ---\n%s", want, body)
		}
	}
}
