package websearch

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSearchAndFormat(t *testing.T) {
	var gotAuth string
	var gotReq searchRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		require.NoError(t, json.NewDecoder(r.Body).Decode(&gotReq))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"code": 200,
			"data": {
				"webPages": {
					"value": [
						{"name": "Result One", "url": "https://example.com/1", "snippet": "0123456789abcdef"},
						{"name": "Result Two", "url": "https://example.com/2", "snippet": "short"}
					]
				}
			}
		}`))
	}))
	defer srv.Close()

	c := New("secret-key", srv.Client())
	c.endpoint = srv.URL

	results, err := c.Search(context.Background(), "forsen record", 3)
	require.NoError(t, err)
	assert.Equal(t, "Bearer secret-key", gotAuth)
	assert.Equal(t, "forsen record", gotReq.Query)
	assert.Equal(t, 3, gotReq.Count)
	require.Len(t, results, 2)

	full := Format(results, 0)
	assert.Contains(t, full, "0123456789abcdef")

	clipped := Format(results, 10)
	assert.Contains(t, clipped, "0123456789…")
	assert.NotContains(t, clipped, "abcdef")
	assert.True(t, strings.Contains(clipped, "[2] Result Two"))
}

func TestFormatEmpty(t *testing.T) {
	assert.Equal(t, "no search results found", Format(nil, 200))
}
