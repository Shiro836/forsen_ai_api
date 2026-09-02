// Package seventv is a minimal read-only client for the 7TV emote API and CDN.
// Emote metadata and the popularity crawl come from v4 GQL, channel emote sets
// from v3 REST — the split third-party clients converge on.
package seventv

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	DefaultAPIURL = "https://7tv.io/v3"
	DefaultGQLURL = "https://7tv.io/v4/gql"
	DefaultCDNURL = "https://cdn.7tv.app"

	// File4x is the largest size 7TV renders. For a static emote it is already
	// the single frame — 7TV lists no separate 4x_static.webp for those.
	File4x = "4x.webp"

	// GlobalEmoteSet is the id of the set every channel gets. It is aliased and
	// carries zero-width entries like any other set.
	GlobalEmoteSet = "global"
)

// v3 emote flag bits. 7TV moderates pornography by unlisting rather than
// flagging — FlagContentSexual is set on 2 of 1.36M emotes — so these are stored
// as facts and never trusted as a moderation signal on their own.
const (
	FlagPrivate                 = 1 << 0
	FlagZeroWidth               = 1 << 8
	FlagContentSexual           = 1 << 16
	FlagContentEpilepsy         = 1 << 17
	FlagContentEdgy             = 1 << 18
	FlagContentTwitchDisallowed = 1 << 24
)

// zeroWidthEntry is the set-entry flag bit. It is authoritative for rendering;
// the emote's own FlagZeroWidth is only a recommendation.
const zeroWidthEntry = 1 << 0

var (
	// ErrNotFound is returned when 7TV has no emote with the requested id.
	ErrNotFound = errors.New("seventv: emote not found")

	// ErrNoAccount means the Twitch user has never linked 7TV. It is an ordinary
	// outcome — the channel simply has no 7TV set — not a failure.
	ErrNoAccount = errors.New("seventv: twitch user has no 7tv account")
)

type Config struct {
	APIURL  string        `yaml:"api_url"`
	GQLURL  string        `yaml:"gql_url"`
	CDNURL  string        `yaml:"cdn_url"`
	Timeout time.Duration `yaml:"timeout"`
}

type Client struct {
	apiURL string
	gqlURL string
	cdnURL string
	http   *http.Client
}

// Emote is the subset of 7TV emote metadata the moderation pipeline needs.
type Emote struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Listed   bool     `json:"listed"`
	Animated bool     `json:"animated"`
	Flags    int      `json:"flags"`
	Tags     []string `json:"tags"`
	Host     Host     `json:"host"`

	// Deleted is only reported by v4 GQL; the v3 REST emote endpoint omits it and
	// answers 404 instead.
	Deleted bool `json:"-"`
}

type Host struct {
	URL   string      `json:"url"`
	Files []ImageFile `json:"files"`
}

type ImageFile struct {
	Name       string `json:"name"`
	StaticName string `json:"static_name"`
	Width      int    `json:"width"`
	Height     int    `json:"height"`
	FrameCount int    `json:"frame_count"`
	Size       int    `json:"size"`
	Format     string `json:"format"`
}

// FrameCount reports the frame count 7TV published for a rendered file, or 0
// when it listed none. ffprobe cannot answer this for animated webp.
func (e *Emote) FrameCount(file string) int {
	for _, f := range e.Host.Files {
		if f.Name == file {
			return f.FrameCount
		}
	}
	return 0
}

// PopularSource ranks emotes by all-time use. An empty query ranks everything; a
// query narrows it, which is how a channel's own emotes reach the cache without
// crawling that channel's set.
type PopularSource interface {
	TopEmotes(ctx context.Context, query string, n int) ([]Emote, error)
}

func New(cfg *Config) *Client {
	apiURL, gqlURL, cdnURL := DefaultAPIURL, DefaultGQLURL, DefaultCDNURL
	timeout := 30 * time.Second
	if cfg != nil {
		if cfg.APIURL != "" {
			apiURL = cfg.APIURL
		}
		if cfg.GQLURL != "" {
			gqlURL = cfg.GQLURL
		}
		if cfg.CDNURL != "" {
			cdnURL = cfg.CDNURL
		}
		if cfg.Timeout > 0 {
			timeout = cfg.Timeout
		}
	}
	return &Client{
		apiURL: strings.TrimSuffix(apiURL, "/"),
		gqlURL: gqlURL,
		cdnURL: strings.TrimSuffix(cdnURL, "/"),
		http:   &http.Client{Timeout: timeout},
	}
}

func (c *Client) getJSON(ctx context.Context, url string, notFound error, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("build request %s: %w", url, err)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("get %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return notFound
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("get %s: unexpected status %d", url, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(dst); err != nil {
		return fmt.Errorf("decode %s: %w", url, err)
	}
	return nil
}

func (c *Client) GetEmote(ctx context.Context, id string) (*Emote, error) {
	var emote Emote
	url := fmt.Sprintf("%s/emotes/%s", c.apiURL, id)
	if err := c.getJSON(ctx, url, fmt.Errorf("emote %s: %w", id, ErrNotFound), &emote); err != nil {
		return nil, err
	}
	if emote.ID == "" {
		emote.ID = id
	}
	return &emote, nil
}

// DownloadImage fetches one rendered file (e.g. File4x) for an emote.
func (c *Client) DownloadImage(ctx context.Context, id, file string) ([]byte, error) {
	url := fmt.Sprintf("%s/emote/%s/%s", c.cdnURL, id, file)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build cdn request: %w", err)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download %s/%s: %w", id, file, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("download %s/%s: %w", id, file, ErrNotFound)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download %s/%s: unexpected status %d", id, file, resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read %s/%s: %w", id, file, err)
	}
	return data, nil
}
