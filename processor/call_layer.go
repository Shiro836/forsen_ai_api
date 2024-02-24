package processor

import (
	"app/ai"
	"app/char"
	"app/db"
	"app/processor/scripts"
	"app/rvc"
	"app/swearfilter"
	"app/tts"
	"context"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"
)

var _ scripts.CallLayer = &callLayerImpl{}

type callLayerImpl struct {
	broadcaster *db.UserData

	rvc *rvc.Client
	ai  *ai.Client
	tts *tts.Client

	db DB
}

func (p *callLayerImpl) GetBroadcaster() string {
	return p.broadcaster.UserLoginData.UserName
}

func (p *callLayerImpl) GetCharCard(ctx context.Context, charName string) (*char.Card, error) {
	return p.db.GetCharCard(ctx, p.broadcaster.UserLoginData.UserName, charName)
}

func (p *callLayerImpl) CallAI(ctx context.Context, prompt string) (string, error) {
	return p.ai.Ask(ctx, prompt)
}

func (p *callLayerImpl) CallTtsText(ctx context.Context, charName string, text string) error {
	panic("not implemented")
}

func (p *callLayerImpl) GetNextEvent(ctx context.Context) (*scripts.Event, error) {
	msg, err := p.db.GetNextMsg(ctx, p.broadcaster.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to get next msg: %w", err)
	}

	return &scripts.Event{
		MsgID: msg.ID,

		UserName:       msg.UserName,
		Message:        msg.Message,
		CustomRewardID: msg.CustomRewardID,
	}, nil
}

func (p *callLayerImpl) FilterText(text string) string {
	var swears []string

	filters, err := db.GetFilters(p.broadcaster.ID)
	if err == nil {
		slices.Concat(swears, strings.Split(filters, ","))
	}

	slices.Concat(swears, swearfilter.Swears)

	swearFilterObj := swearfilter.NewSwearFilter(false, swears...)

	filtered := text

	tripped, _ := swearFilterObj.Check(text)
	for _, word := range tripped {
		filtered = IReplace(filtered, word, strings.Repeat("*", len(word)))
	}

	return filtered
}

func IReplace(s, old, new string) string { // replace all, case insensitive
	if old == new || old == "" {
		return s // avoid allocation
	}
	t := strings.ToLower(s)
	o := strings.ToLower(old)

	// Compute number of replacements.
	n := strings.Count(t, o)
	if n == 0 {
		return s // avoid allocation
	}
	// Apply replacements to buffer.
	var b strings.Builder
	b.Grow(len(s) + n*(len(new)-len(old)))
	start := 0
	for i := 0; i < n; i++ {
		j := start
		if len(old) == 0 {
			if i > 0 {
				_, wid := utf8.DecodeRuneInString(s[start:])
				j += wid
			}
		} else {
			j += strings.Index(t[start:], o)
		}
		b.WriteString(s[start:j])
		b.WriteString(new)
		start = j + len(old)
	}
	b.WriteString(s[start:])
	return b.String()
}
