package seventv

import (
	"context"
	"fmt"
)

// ActiveEmote is one entry of an emote set. Name is the channel's alias, which
// differs from the emote's own name for roughly one entry in six — including in
// the global set — so word lookup must key on Name and moderation on EmoteID.
type ActiveEmote struct {
	EmoteID   string
	Name      string
	BaseName  string
	ZeroWidth bool
	Emote     Emote
}

// Aliased reports whether the channel renamed the emote.
func (a ActiveEmote) Aliased() bool { return a.Name != a.BaseName }

type EmoteSet struct {
	ID     string
	Name   string
	Emotes []ActiveEmote
}

// TwitchUser is a Twitch channel's 7TV account and its active emote set.
type TwitchUser struct {
	TwitchID   string
	Username   string
	EmoteSetID string
	Set        EmoteSet
}

type activeEmoteJSON struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Flags int    `json:"flags"`
	Data  Emote  `json:"data"`
}

type emoteSetJSON struct {
	ID     string            `json:"id"`
	Name   string            `json:"name"`
	Emotes []activeEmoteJSON `json:"emotes"`
}

func (s emoteSetJSON) toSet() EmoteSet {
	out := EmoteSet{ID: s.ID, Name: s.Name, Emotes: make([]ActiveEmote, 0, len(s.Emotes))}
	for _, e := range s.Emotes {
		out.Emotes = append(out.Emotes, ActiveEmote{
			EmoteID:   e.ID,
			Name:      e.Name,
			BaseName:  e.Data.Name,
			ZeroWidth: e.Flags&zeroWidthEntry != 0,
			Emote:     e.Data,
		})
	}
	return out
}

type twitchUserJSON struct {
	ID         string       `json:"id"`
	Username   string       `json:"username"`
	EmoteSetID string       `json:"emote_set_id"`
	EmoteSet   emoteSetJSON `json:"emote_set"`
}

// GetTwitchUser resolves a Twitch channel's 7TV account and returns its active
// emote set inline — one request, no follow-up for the set. ErrNoAccount means
// the channel has no 7TV emotes, which is not an error condition.
func (c *Client) GetTwitchUser(ctx context.Context, twitchID string) (*TwitchUser, error) {
	var parsed twitchUserJSON
	url := fmt.Sprintf("%s/users/twitch/%s", c.apiURL, twitchID)
	if err := c.getJSON(ctx, url, fmt.Errorf("twitch user %s: %w", twitchID, ErrNoAccount), &parsed); err != nil {
		return nil, err
	}
	return &TwitchUser{
		TwitchID:   parsed.ID,
		Username:   parsed.Username,
		EmoteSetID: parsed.EmoteSetID,
		Set:        parsed.EmoteSet.toSet(),
	}, nil
}

func (c *Client) GetEmoteSet(ctx context.Context, setID string) (*EmoteSet, error) {
	var parsed emoteSetJSON
	url := fmt.Sprintf("%s/emote-sets/%s", c.apiURL, setID)
	if err := c.getJSON(ctx, url, fmt.Errorf("emote set %s: %w", setID, ErrNotFound), &parsed); err != nil {
		return nil, err
	}
	set := parsed.toSet()
	return &set, nil
}

func (c *Client) GetGlobalSet(ctx context.Context) (*EmoteSet, error) {
	return c.GetEmoteSet(ctx, GlobalEmoteSet)
}
