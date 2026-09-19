package ingest

import (
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/google/uuid"
)

type feed int

const (
	feedChat feed = iota
	feedEventSub
)

const correlatorTTL = 5 * time.Minute

// Twitch gives the two feeds no shared id, so an event is recognised by who
// did what with which text; what is a reward id, a cheer of some bits, a resub.
type pairKey struct {
	broadcasterID int
	viewerID      int
	what          string
	text          string
}

func newPairKey(broadcasterID, viewerID int, what, text string) pairKey {
	text = strings.Map(func(r rune) rune {
		// U+E0000 is what chat clients append to get past the duplicate-message
		// check; it is unassigned, so the format-character class misses it.
		if unicode.Is(unicode.Cf, r) || r == 0xE0000 {
			return -1
		}
		return r
	}, text)

	return pairKey{
		broadcasterID: broadcasterID,
		viewerID:      viewerID,
		what:          what,
		text:          strings.Join(strings.Fields(text), " "),
	}
}

type unpairedRow struct {
	msgID   uuid.UUID
	arrived time.Time
}

// All rows waiting under one key came from the same feed: an arrival from the
// other feed takes the oldest instead of joining them.
type unpaired struct {
	from feed
	rows []unpairedRow
}

// correlator pairs the two arrivals of one event. Both feeds keep their
// order per channel, so the n-th repeat on one pairs with the n-th on the other.
type correlator struct {
	lock    sync.Mutex
	waiting map[pairKey]*unpaired
	swept   time.Time
}

func newCorrelator() *correlator {
	return &correlator{waiting: make(map[pairKey]*unpaired)}
}

// pair returns the row the other feed already created for this event.
// Without one it runs create and keeps the new row for the other feed to find.
// The lock spans create: the twin arriving meanwhile must see this row.
func (c *correlator) pair(key pairKey, from feed, now time.Time, create func() (uuid.UUID, error)) (twin uuid.UUID, paired bool, err error) {
	c.lock.Lock()
	defer c.lock.Unlock()

	c.sweep(now)

	entry := c.waiting[key]
	if entry != nil && entry.from != from && len(entry.rows) > 0 {
		twin = entry.rows[0].msgID
		entry.rows = entry.rows[1:]
		if len(entry.rows) == 0 {
			delete(c.waiting, key)
		}
		return twin, true, nil
	}

	msgID, err := create()
	if err != nil {
		return uuid.Nil, false, err
	}

	if entry == nil {
		entry = &unpaired{from: from}
		c.waiting[key] = entry
	}
	entry.rows = append(entry.rows, unpairedRow{msgID: msgID, arrived: now})

	return uuid.Nil, false, nil
}

func (c *correlator) sweep(now time.Time) {
	if now.Sub(c.swept) < time.Minute {
		return
	}
	c.swept = now

	for key, entry := range c.waiting {
		fresh := entry.rows[:0]
		for _, row := range entry.rows {
			if now.Sub(row.arrived) < correlatorTTL {
				fresh = append(fresh, row)
			}
		}
		entry.rows = fresh
		if len(entry.rows) == 0 {
			delete(c.waiting, key)
		}
	}
}
