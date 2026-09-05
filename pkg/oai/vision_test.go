package oai

import (
	"app/pkg/llm"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The schema is what enforces the format — json_object mode is accepted and
// ignored by llama-server — so it has to reach the wire as a JSON object rather
// than as the base64 a []byte would otherwise serialize to.
func TestAskVisionJSONSendsTheSchemaAsAGrammar(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(raw, &body))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{}"}}]}`))
	}))
	defer server.Close()

	schema := json.RawMessage(`{"type":"object","required":["description"]}`)
	_, err := New(&llm.Config{URL: server.URL, Model: "qwen36-hauhau", MaxTokens: 512}).
		AskVisionJSON(context.Background(), "judge this", []Image{{MIME: "image/png", Data: []byte{1, 2}}}, schema, 0)
	require.NoError(t, err)

	format, ok := body["response_format"].(map[string]any)
	require.True(t, ok, "response_format: %v", body["response_format"])
	assert.Equal(t, "json_schema", format["type"])

	sent, err := json.Marshal(format["json_schema"].(map[string]any)["schema"])
	require.NoError(t, err)
	assert.JSONEq(t, string(schema), string(sent))
}
