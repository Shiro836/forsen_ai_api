package ingest

import (
	"context"
	"log/slog"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Its-donkey/kappopher/helix"
)

const cheermotesTTL = 6 * time.Hour

type cheermoteWords struct {
	word    *regexp.Regexp
	fetched time.Time
}

// cheermotes knows the words that spend bits in a cheer ("Cheer100"). A
// channel can have its own next to Twitch's, so they are asked per channel.
type cheermotes struct {
	logger *slog.Logger
	app    *appClient

	lock    sync.Mutex
	channel map[int]cheermoteWords
}

func newCheermotes(logger *slog.Logger, app *appClient) *cheermotes {
	return &cheermotes{logger: logger, app: app, channel: make(map[int]cheermoteWords)}
}

func cheermoteWord(prefixes []string) *regexp.Regexp {
	quoted := make([]string, len(prefixes))
	for i, prefix := range prefixes {
		quoted[i] = regexp.QuoteMeta(prefix)
	}
	return regexp.MustCompile(`(?i)^(?:` + strings.Join(quoted, "|") + `)\d+$`)
}

// strip leaves only what the viewer wants said. Words that cannot be looked
// up stay in: a paid message with "Cheer100" read out beats one not played.
func (c *cheermotes) strip(ctx context.Context, broadcasterID int, text string) string {
	c.lock.Lock()
	words := c.channel[broadcasterID]
	c.lock.Unlock()

	if time.Since(words.fetched) > cheermotesTTL {
		var resp *helix.Response[helix.Cheermote]
		err := c.app.call(ctx, func() (err error) {
			resp, err = c.app.GetCheermotes(ctx, strconv.Itoa(broadcasterID))
			return err
		})
		if err != nil {
			c.logger.Error("failed to get cheermotes", "broadcaster", broadcasterID, "err", err)
		} else {
			prefixes := make([]string, len(resp.Data))
			for i, cheermote := range resp.Data {
				prefixes[i] = cheermote.Prefix
			}
			words = cheermoteWords{word: cheermoteWord(prefixes), fetched: time.Now()}

			c.lock.Lock()
			c.channel[broadcasterID] = words
			c.lock.Unlock()
		}
	}

	if words.word == nil {
		return text
	}

	said := slices.DeleteFunc(strings.Fields(text), words.word.MatchString)
	return strings.Join(said, " ")
}
