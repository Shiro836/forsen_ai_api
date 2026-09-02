package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"app/internal/emoteservice"
)

// emoteClient talks to the emote moderation service. The shared secret grants
// read/write over every channel's lists, so it stays server-side: the browser
// only ever reaches the handlers in emotes.go.
type emoteClient struct {
	baseURL string
	token   string
	http    *http.Client
}

// newEmoteClient returns nil when the emote service is not configured, which is
// how the panel knows to render an "unavailable" notice instead of failing.
func newEmoteClient(cfg *emoteservice.Config) *emoteClient {
	if cfg == nil || cfg.SharedSecret == "" {
		return nil
	}

	host := cfg.Host
	if host == "" {
		host = "127.0.0.1"
	}
	port := cfg.Port
	if port == 0 {
		port = 8086
	}

	return &emoteClient{
		baseURL: fmt.Sprintf("http://%s:%d", host, port),
		token:   cfg.SharedSecret,
		http:    &http.Client{Timeout: 60 * time.Second},
	}
}

type emoteErrorResponse struct {
	Error string `json:"error"`
}

// emoteHTTPError carries the service's status code so callers can tell a 404
// from a delete ("nothing to remove") apart from a real failure.
type emoteHTTPError struct {
	Status  int
	Message string
}

func (e *emoteHTTPError) Error() string { return e.Message }

func (c *emoteClient) do(ctx context.Context, method, path string, body, out any) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode %s %s: %w", method, path, err)
		}
		reader = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return fmt.Errorf("build %s %s: %w", method, path, err)
	}
	req.Header.Set(emoteservice.AuthHeader, c.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("call %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	payload, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return fmt.Errorf("read %s %s: %w", method, path, err)
	}

	if resp.StatusCode >= http.StatusMultipleChoices {
		msg := fmt.Sprintf("%s %s: status %d", method, path, resp.StatusCode)
		var e emoteErrorResponse
		if json.Unmarshal(payload, &e) == nil && e.Error != "" {
			msg = fmt.Sprintf("%s %s: %s", method, path, e.Error)
		}
		return &emoteHTTPError{Status: resp.StatusCode, Message: msg}
	}

	if out == nil {
		return nil
	}
	if err := json.Unmarshal(payload, out); err != nil {
		return fmt.Errorf("decode %s %s: %w", method, path, err)
	}
	return nil
}

func (c *emoteClient) ChannelSet(ctx context.Context, twitchUserID int) (*emoteservice.ChannelSet, error) {
	var out emoteservice.ChannelSet
	path := "/v1/channel-set?twitch_id=" + strconv.Itoa(twitchUserID)
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *emoteClient) GlobalSet(ctx context.Context) (*emoteservice.EmoteSetResponse, error) {
	var out emoteservice.EmoteSetResponse
	if err := c.do(ctx, http.MethodGet, "/v1/global-set", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// enqueueSetResult is what a whole-set classification request achieved.
type enqueueSetResult struct {
	SetID  string `json:"set_id"`
	Emotes int    `json:"emotes"`
	Queued int    `json:"queued"`
}

// EnqueueSet classifies every emote of one set. Exactly one of setID and
// twitchUserID is used; twitchUserID resolves the channel's active set.
func (c *emoteClient) EnqueueSet(ctx context.Context, setID string, twitchUserID, priority int) (*enqueueSetResult, error) {
	body := map[string]any{"priority": priority}
	if setID != "" {
		body["set_id"] = setID
	} else {
		body["twitch_id"] = strconv.Itoa(twitchUserID)
	}

	var out enqueueSetResult
	if err := c.do(ctx, http.MethodPost, "/v1/enqueue-set", body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *emoteClient) Verdicts(ctx context.Context, streamerID string, emoteIDs []string) (map[string]emoteservice.Decision, error) {
	var out struct {
		Verdicts map[string]emoteservice.Decision `json:"verdicts"`
	}
	body := map[string]any{"streamer_id": streamerID, "emote_ids": emoteIDs}
	if err := c.do(ctx, http.MethodPost, "/v1/verdicts", body, &out); err != nil {
		return nil, err
	}
	return out.Verdicts, nil
}

func (c *emoteClient) SearchEmotes(ctx context.Context, query string, limit int) ([]*emoteservice.Emote, error) {
	var out struct {
		Emotes []*emoteservice.Emote `json:"emotes"`
	}
	path := fmt.Sprintf("/v1/emotes?q=%s&limit=%d", url.QueryEscape(query), limit)
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return out.Emotes, nil
}

// SemanticSearch ranks the classified registry by meaning rather than by name.
// It needs the embedding server, so it fails while name search still works.
func (c *emoteClient) SemanticSearch(ctx context.Context, query string, limit int) ([]emoteservice.Candidate, error) {
	var out struct {
		Results []emoteservice.Candidate `json:"results"`
	}
	path := fmt.Sprintf("/v1/search?q=%s&limit=%d", url.QueryEscape(query), limit)
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return out.Results, nil
}

func (c *emoteClient) Enqueue(ctx context.Context, emoteIDs []string, priority int) (int, error) {
	var out struct {
		Queued int `json:"queued"`
	}
	body := map[string]any{"emote_ids": emoteIDs, "priority": priority}
	if err := c.do(ctx, http.MethodPost, "/v1/enqueue", body, &out); err != nil {
		return 0, err
	}
	return out.Queued, nil
}

func (c *emoteClient) QueueStats(ctx context.Context) (*emoteservice.QueueStats, error) {
	var out emoteservice.QueueStats
	if err := c.do(ctx, http.MethodGet, "/v1/queue", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Reclassify re-queues emotes judged under an older taxonomy; all forces every
// classified emote through the vision model again.
func (c *emoteClient) Reclassify(ctx context.Context, all bool) (int, error) {
	var out struct {
		Enqueued int `json:"enqueued"`
	}
	body := map[string]any{"all": all, "priority": emotePrioritySet}
	if err := c.do(ctx, http.MethodPost, "/v1/reclassify", body, &out); err != nil {
		return 0, err
	}
	return out.Enqueued, nil
}

func (c *emoteClient) ResetSettings(ctx context.Context, streamerID string) (emoteservice.Settings, error) {
	var out emoteservice.Settings
	err := c.do(ctx, http.MethodDelete, "/v1/settings?streamer_id="+url.QueryEscape(streamerID), nil, &out)
	return out, err
}

func (c *emoteClient) GetPlatformSettings(ctx context.Context) ([]string, error) {
	var out struct {
		BlockedClasses []string `json:"blocked_classes"`
	}
	if err := c.do(ctx, http.MethodGet, "/v1/platform-settings", nil, &out); err != nil {
		return nil, err
	}
	return out.BlockedClasses, nil
}

func (c *emoteClient) PutPlatformSettings(ctx context.Context, blocked []string) error {
	return c.do(ctx, http.MethodPut, "/v1/platform-settings", map[string]any{"blocked_classes": blocked}, nil)
}

func (c *emoteClient) AddOverride(ctx context.Context, o emoteservice.Override) error {
	return c.do(ctx, http.MethodPost, "/v1/overrides", o, nil)
}

func (c *emoteClient) DeleteOverride(ctx context.Context, o emoteservice.Override) error {
	return c.do(ctx, http.MethodDelete, "/v1/overrides", o, nil)
}

func (c *emoteClient) GetSettings(ctx context.Context, streamerID string) (emoteservice.Settings, error) {
	var out emoteservice.Settings
	err := c.do(ctx, http.MethodGet, "/v1/settings?streamer_id="+url.QueryEscape(streamerID), nil, &out)
	return out, err
}

func (c *emoteClient) PutSettings(ctx context.Context, s emoteservice.Settings) error {
	return c.do(ctx, http.MethodPut, "/v1/settings", s, nil)
}

func (c *emoteClient) ListRules(ctx context.Context, streamerID string) ([]emoteservice.Rule, error) {
	var out struct {
		Rules []emoteservice.Rule `json:"rules"`
	}
	if err := c.do(ctx, http.MethodGet, "/v1/rules?streamer_id="+url.QueryEscape(streamerID), nil, &out); err != nil {
		return nil, err
	}
	return out.Rules, nil
}

// rulePreview is a rule plus the emotes it proposes to block. PreviewError
// carries an embedder outage without losing the rule that was just created.
type rulePreview struct {
	Rule         *emoteservice.Rule       `json:"rule"`
	Candidates   []emoteservice.Candidate `json:"candidates"`
	PreviewError string                   `json:"preview_error"`
}

func (c *emoteClient) CreateRule(ctx context.Context, streamerID, ruleText string, threshold *float64) (*rulePreview, error) {
	var out rulePreview
	body := map[string]any{"streamer_id": streamerID, "rule_text": ruleText, "threshold": threshold}
	if err := c.do(ctx, http.MethodPost, "/v1/rules", body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *emoteClient) PreviewRule(ctx context.Context, ruleID string) (*rulePreview, error) {
	var out rulePreview
	if err := c.do(ctx, http.MethodPost, "/v1/rules/"+url.PathEscape(ruleID)+"/preview", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *emoteClient) ConfirmRule(ctx context.Context, ruleID string, emoteIDs []string) (int, error) {
	var out struct {
		Blocked int `json:"blocked"`
	}
	body := map[string]any{"emote_ids": emoteIDs}
	if err := c.do(ctx, http.MethodPost, "/v1/rules/"+url.PathEscape(ruleID)+"/confirm", body, &out); err != nil {
		return 0, err
	}
	return out.Blocked, nil
}

func (c *emoteClient) DeleteRule(ctx context.Context, ruleID string) error {
	return c.do(ctx, http.MethodDelete, "/v1/rules/"+url.PathEscape(ruleID), nil, nil)
}
