package whisperx

import (
	"app/pkg/tools"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

type Config struct {
	URL string
}

type Client struct {
	httpClient HTTPClient
	cfg        *Config
}

func New(httpClient HTTPClient, cfg *Config) *Client {
	return &Client{
		httpClient: httpClient,
		cfg:        cfg,
	}
}

type Timiing struct {
	Text  string
	Start time.Duration
	End   time.Duration
}

type transcript struct {
	Text  string  `json:"text"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
}

type alignRequest struct {
	Audio      []byte       `json:"audio"`
	Transcript []transcript `json:"transcript"`
}

type word struct {
	Word  string  `json:"word"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
}

type Segment struct {
	Words []word `json:"words"`
}

type alignResponse struct {
	Segments []Segment `json:"segments"`
}

func (c *Client) Align(ctx context.Context, text string, audio []byte, audioLen time.Duration) ([]Timiing, error) {
	return c.AlignSegments(ctx, []Timiing{{Text: text, Start: 0, End: audioLen}}, audio)
}

// AlignSegments aligns each segment's text against its own [Start, End] slice
// of the audio. The service caps audio length per slice, not per file, so
// pre-segmented long audio aligns where a whole-file request would be rejected.
func (c *Client) AlignSegments(ctx context.Context, segments []Timiing, audio []byte) ([]Timiing, error) {
	transcripts := make([]transcript, 0, len(segments))
	for _, seg := range segments {
		transcripts = append(transcripts, transcript{
			Text:  seg.Text,
			Start: seg.Start.Seconds(),
			End:   seg.End.Seconds(),
		})
	}

	reqBody, err := json.Marshal(alignRequest{
		Audio:      audio,
		Transcript: transcripts,
	})
	if err != nil {
		return nil, err
	}

	url, err := url.Parse(c.cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("failed to parse url: %w", err)
	}

	url.Path = "/align"

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url.String(), bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create http request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to send http request: %w", err)
	}
	defer tools.DrainAndClose(resp.Body)

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to send http request: %s", resp.Status)
	}

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read http response body: %w", err)
	}

	var alignResponse alignResponse
	if err := json.Unmarshal(respBody, &alignResponse); err != nil {
		return nil, fmt.Errorf("failed to unmarshal http response body: %w", err)
	}

	timings := make([]Timiing, 0, 60)

	for _, segment := range alignResponse.Segments {
		for _, word := range segment.Words {
			timings = append(timings, Timiing{
				Text:  word.Word,
				Start: time.Duration(word.Start * float64(time.Second)),
				End:   time.Duration(word.End * float64(time.Second)),
			})
		}
	}

	return timings, nil
}
