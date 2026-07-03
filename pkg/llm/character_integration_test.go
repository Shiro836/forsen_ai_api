//go:build integration

package llm

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestChatClient_Live hits the running cydonia service (llm_text2 on :3334) and
// checks a real reply comes back clean: non-empty, stays on one turn, and obeys
// the TTS guard (no asterisk stage directions).
//
//	go test -tags integration ./pkg/llm/ -run Live -v
func TestChatClient_Live(t *testing.T) {
	c := ChatClient{Client: New(
		&http.Client{Timeout: 60 * time.Second},
		&Config{URL: "http://localhost:3334", Model: "cydonia", MaxTokens: 120, MinTokens: 1},
	)}

	out, err := c.CharacterReply(context.Background(), testCard(), "tester", "say something nice to me for once", nil)
	if err != nil {
		t.Fatalf("live cydonia call failed (is llm_text2 up on :3334?): %v", err)
	}
	t.Logf("cydonia reply: %s", out)

	if strings.TrimSpace(out) == "" {
		t.Fatal("empty reply")
	}
	if strings.Contains(out, "*") {
		t.Errorf("TTS guard violated — reply contains asterisk stage direction: %q", out)
	}
}
