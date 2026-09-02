// Package infinity is a client for an Infinity embedding server's
// OpenAI-compatible API. It serves one CLIP-style model, so text and image
// embeddings land in the same space and can be compared directly.
package infinity

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

type Config struct {
	URL    string `yaml:"url"`
	APIKey string `yaml:"api_key"`
	Model  string `yaml:"model"`
	// Dimension is the model's output width. It is checked against every
	// response so a model swap fails loudly instead of writing vectors that no
	// longer compare against the cached ones.
	Dimension int           `yaml:"dimension"`
	Timeout   time.Duration `yaml:"timeout"`
}

// Image is one image to embed.
type Image struct {
	MIME string
	Data []byte
}

type Client struct {
	baseURL   string
	apiKey    string
	model     string
	dimension int
	http      *http.Client
}

func New(cfg *Config) *Client {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	return &Client{
		baseURL:   strings.TrimSuffix(cfg.URL, "/"),
		apiKey:    cfg.APIKey,
		model:     cfg.Model,
		dimension: cfg.Dimension,
		http:      &http.Client{Timeout: timeout},
	}
}

func (c *Client) EmbedText(ctx context.Context, texts []string) ([][]float32, error) {
	input := make([]string, len(texts))
	copy(input, texts)
	return c.embed(ctx, input, "")
}

// EmbedImages sends images to the same /embeddings route as text, tagged with
// modality; the separate /embeddings_image route is deprecated.
func (c *Client) EmbedImages(ctx context.Context, images []Image) ([][]float32, error) {
	input := make([]string, 0, len(images))
	for _, img := range images {
		mime := img.MIME
		if mime == "" {
			mime = "image/png"
		}
		input = append(input, fmt.Sprintf("data:%s;base64,%s", mime, base64.StdEncoding.EncodeToString(img.Data)))
	}
	return c.embed(ctx, input, "image")
}

type embedRequest struct {
	Model    string   `json:"model"`
	Input    []string `json:"input"`
	Modality string   `json:"modality,omitempty"`
}

type embedResponse struct {
	Data []struct {
		Index     int       `json:"index"`
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
}

func (c *Client) embed(ctx context.Context, input []string, modality string) ([][]float32, error) {
	if len(input) == 0 {
		return nil, nil
	}

	const path = "/embeddings"

	body, err := json.Marshal(embedRequest{Model: c.model, Input: input, Modality: modality})
	if err != nil {
		return nil, fmt.Errorf("marshal embed request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build embed request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embed %s: %w", path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("embed %s: unexpected status %d", path, resp.StatusCode)
	}

	var parsed embedResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("decode embed response: %w", err)
	}
	if len(parsed.Data) != len(input) {
		return nil, fmt.Errorf("embed %s: got %d embeddings for %d inputs", path, len(parsed.Data), len(input))
	}

	sort.Slice(parsed.Data, func(i, j int) bool { return parsed.Data[i].Index < parsed.Data[j].Index })

	out := make([][]float32, 0, len(parsed.Data))
	for _, d := range parsed.Data {
		if c.dimension > 0 && len(d.Embedding) != c.dimension {
			return nil, fmt.Errorf("embed %s: model returned %d dims, config expects %d", path, len(d.Embedding), c.dimension)
		}
		out = append(out, d.Embedding)
	}
	return out, nil
}
