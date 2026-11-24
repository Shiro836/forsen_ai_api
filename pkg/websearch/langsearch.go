// Package websearch queries the LangSearch web-search API and formats results
// for LLM tool calls.
package websearch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"app/pkg/tools"
)

const defaultEndpoint = "https://api.langsearch.com/v1/web-search"

// Result is a single web page returned by LangSearch.
type Result struct {
	Name    string
	URL     string
	Snippet string
}

// Client queries the LangSearch web-search API.
type Client struct {
	apiKey     string
	endpoint   string
	httpClient *http.Client
}

// New creates a LangSearch client. A nil httpClient uses a 20s timeout.
func New(apiKey string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 20 * time.Second}
	}
	return &Client{
		apiKey:     apiKey,
		endpoint:   defaultEndpoint,
		httpClient: httpClient,
	}
}

type searchRequest struct {
	Query   string `json:"query"`
	Count   int    `json:"count"`
	Summary bool   `json:"summary"`
}

type searchResponse struct {
	Code int `json:"code"`
	Data struct {
		WebPages struct {
			Value []struct {
				Name    string `json:"name"`
				URL     string `json:"url"`
				Snippet string `json:"snippet"`
			} `json:"value"`
		} `json:"webPages"`
	} `json:"data"`
}

// Search queries LangSearch for up to count web pages (defaults to 5).
func (c *Client) Search(ctx context.Context, query string, count int) ([]Result, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}
	if count <= 0 {
		count = 5
	}

	body, err := json.Marshal(searchRequest{
		Query:   query,
		Count:   count,
		Summary: false,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal search request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create search request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("execute search request: %w", err)
	}
	defer tools.DrainAndClose(resp.Body)

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, fmt.Errorf("read search response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("langsearch status %d", resp.StatusCode)
	}

	var parsed searchResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, fmt.Errorf("parse search response: %w", err)
	}

	results := make([]Result, 0, len(parsed.Data.WebPages.Value))
	for _, p := range parsed.Data.WebPages.Value {
		results = append(results, Result{
			Name:    p.Name,
			URL:     p.URL,
			Snippet: p.Snippet,
		})
	}
	return results, nil
}

// Format renders search results as numbered text entries. When maxSnippetRunes
// is positive, each snippet is clipped to bound LLM context growth.
func Format(results []Result, maxSnippetRunes int) string {
	if len(results) == 0 {
		return "no search results found"
	}
	var sb strings.Builder
	for i, p := range results {
		if i > 0 {
			sb.WriteString("\n\n")
		}
		snippet := strings.TrimSpace(p.Snippet)
		if maxSnippetRunes > 0 {
			r := []rune(snippet)
			if len(r) > maxSnippetRunes {
				snippet = string(r[:maxSnippetRunes]) + "…"
			}
		}
		fmt.Fprintf(&sb, "[%d] %s\n%s\n%s", i+1, p.Name, p.URL, snippet)
	}
	return sb.String()
}
