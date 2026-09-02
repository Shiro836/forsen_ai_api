package seventv

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// 7TV rejects perPage above 250 and page above 100, which caps the popularity
// crawl at MaxTopEmotes. Asking for more must fail rather than silently truncate.
const (
	topEmotesPerPage = 250
	topEmotesMaxPage = 100

	MaxTopEmotes = topEmotesPerPage * topEmotesMaxPage
)

const topEmotesQuery = `query TopEmotes($query:String,$sort:Sort!,$page:Int!,$perPage:Int!){emotes{search(query:$query,sort:$sort,page:$page,perPage:$perPage){totalCount pageCount items{id defaultName tags flags{publicListed private nsfw defaultZeroWidth animated} scores{topAllTime} owner{mainConnection{platformUsername}}}}}}`

type gqlRequest struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables,omitempty"`
}

type gqlError struct {
	Message string `json:"message"`
}

type v4Emote struct {
	ID          string   `json:"id"`
	DefaultName string   `json:"defaultName"`
	Tags        []string `json:"tags"`
	Deleted     bool     `json:"deleted"`
	Flags       struct {
		PublicListed     bool `json:"publicListed"`
		Private          bool `json:"private"`
		NSFW             bool `json:"nsfw"`
		DefaultZeroWidth bool `json:"defaultZeroWidth"`
		Animated         bool `json:"animated"`
	} `json:"flags"`
}

// toEmote rebuilds the v3 flags bitfield from v4's booleans so both metadata
// paths store the same fact.
func (e v4Emote) toEmote() Emote {
	flags := 0
	if e.Flags.Private {
		flags |= FlagPrivate
	}
	if e.Flags.DefaultZeroWidth {
		flags |= FlagZeroWidth
	}
	if e.Flags.NSFW {
		flags |= FlagContentSexual
	}
	return Emote{
		ID:       e.ID,
		Name:     e.DefaultName,
		Listed:   e.Flags.PublicListed,
		Animated: e.Flags.Animated,
		Deleted:  e.Deleted,
		Flags:    flags,
		Tags:     e.Tags,
	}
}

type topEmotesResponse struct {
	Data struct {
		Emotes struct {
			Search struct {
				TotalCount int       `json:"totalCount"`
				PageCount  int       `json:"pageCount"`
				Items      []v4Emote `json:"items"`
			} `json:"search"`
		} `json:"emotes"`
	} `json:"data"`
	Errors []gqlError `json:"errors"`
}

// TopEmotes returns the n most-used emotes matching query, best first. An empty
// query ranks the whole database. Pages are fetched sequentially; 7TV's rate
// budget is 5000 requests a minute, so the 100-request worst case costs 2% of
// one window.
func (c *Client) TopEmotes(ctx context.Context, query string, n int) ([]Emote, error) {
	if n <= 0 {
		return nil, nil
	}
	if n > MaxTopEmotes {
		return nil, fmt.Errorf("seventv: top %d emotes requested, 7tv paginates at most %d", n, MaxTopEmotes)
	}

	out := make([]Emote, 0, n)
	seen := make(map[string]struct{}, n)

	for page := 1; page <= topEmotesMaxPage && len(out) < n; page++ {
		variables := map[string]any{
			"sort":    map[string]string{"sortBy": "TOP_ALL_TIME", "order": "DESCENDING"},
			"page":    page,
			"perPage": topEmotesPerPage,
		}
		if query != "" {
			variables["query"] = query
		}

		var parsed topEmotesResponse
		if err := c.gql(ctx, gqlRequest{Query: topEmotesQuery, Variables: variables}, &parsed); err != nil {
			return nil, fmt.Errorf("top emotes page %d for %q: %w", page, query, err)
		}

		items := parsed.Data.Emotes.Search.Items
		if len(items) == 0 {
			break
		}
		for _, item := range items {
			if _, dup := seen[item.ID]; dup {
				continue
			}
			seen[item.ID] = struct{}{}
			out = append(out, item.toEmote())
			if len(out) == n {
				break
			}
		}
	}
	return out, nil
}

func (c *Client) gql(ctx context.Context, request gqlRequest, dst any) error {
	body, err := json.Marshal(request)
	if err != nil {
		return fmt.Errorf("marshal gql request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.gqlURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build gql request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("post gql: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("post gql: unexpected status %d", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(dst); err != nil {
		return fmt.Errorf("decode gql response: %w", err)
	}

	// GraphQL reports failures in the body with a 200 status.
	if errs, ok := dst.(interface{ gqlErrors() []gqlError }); ok {
		if messages := errs.gqlErrors(); len(messages) > 0 {
			texts := make([]string, 0, len(messages))
			for _, m := range messages {
				texts = append(texts, m.Message)
			}
			return fmt.Errorf("gql: %s", strings.Join(texts, "; "))
		}
	}
	return nil
}

func (r *topEmotesResponse) gqlErrors() []gqlError { return r.Errors }
