package emoteservice

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func classified(mut func(*Emote)) *Emote {
	now := time.Now()
	e := &Emote{ID: "e1", Name: "forsenCream", Listed: true, ClassifiedAt: &now}
	if mut != nil {
		mut(e)
	}
	return e
}

func TestDecidePrecedence(t *testing.T) {
	blockAll := Settings{BlockedClasses: DefaultBlockedClasses()}

	tests := []struct {
		name     string
		facts    Facts
		settings Settings
		verdict  Verdict
		reason   string
	}{
		{
			name:     "streamer ban beats every other signal",
			facts:    Facts{Emote: classified(nil), StreamerBan: true, StreamerApprove: true, GlobalBan: true, GlobalApprove: true, RuleID: "r1"},
			settings: blockAll,
			verdict:  VerdictBlock,
			reason:   ReasonStreamerBan,
		},
		{
			name:     "streamer approve beats global ban",
			facts:    Facts{Emote: classified(func(e *Emote) { e.Classes = []string{ClassSexual} }), StreamerApprove: true, GlobalBan: true, RuleID: "r1"},
			settings: blockAll,
			verdict:  VerdictAllow,
			reason:   ReasonStreamerApprove,
		},
		{
			name:     "global ban beats global approve",
			facts:    Facts{Emote: classified(nil), GlobalBan: true, GlobalApprove: true, RuleID: "r1"},
			settings: blockAll,
			verdict:  VerdictBlock,
			reason:   ReasonGlobalBan,
		},
		{
			name:     "global approve beats rule match and auto verdict",
			facts:    Facts{Emote: classified(func(e *Emote) { e.Classes = []string{ClassSexual}; e.Listed = false }), GlobalApprove: true, RuleID: "r1"},
			settings: blockAll,
			verdict:  VerdictAllow,
			reason:   ReasonGlobalApprove,
		},
		{
			name:     "global approve un-blocks a 7tv-unlisted emote",
			facts:    Facts{Emote: classified(func(e *Emote) { e.Listed = false }), GlobalApprove: true},
			settings: blockAll,
			verdict:  VerdictAllow,
			reason:   ReasonGlobalApprove,
		},
		{
			name:     "rule match beats auto verdict",
			facts:    Facts{Emote: classified(nil), RuleID: "r1"},
			settings: blockAll,
			verdict:  VerdictBlock,
			reason:   ReasonRule + ":r1",
		},
		{
			name:     "a blocked class blocks and names itself",
			facts:    Facts{Emote: classified(func(e *Emote) { e.Classes = []string{ClassSexual} })},
			settings: blockAll,
			verdict:  VerdictBlock,
			reason:   ReasonClass + ":" + ClassSexual,
		},
		{
			name:     "a class the channel does not block is allowed",
			facts:    Facts{Emote: classified(func(e *Emote) { e.Classes = []string{ClassSexual} })},
			settings: Settings{BlockedClasses: []string{ClassCum, ClassHate}},
			verdict:  VerdictAllow,
			reason:   ReasonAuto,
		},
		{
			name:     "forsenCream passes in a channel that opted cum back in",
			facts:    Facts{Emote: classified(func(e *Emote) { e.Classes = []string{ClassCum} })},
			settings: Settings{BlockedClasses: []string{ClassSexual, ClassViolence, ClassHate, ClassFlashing, ClassDrugs}},
			verdict:  VerdictAllow,
			reason:   ReasonAuto,
		},
		{
			name:     "the offending class is the alphabetically first one, not the first classified",
			facts:    Facts{Emote: classified(func(e *Emote) { e.Classes = []string{ClassViolence, ClassDrugs, ClassHate} })},
			settings: blockAll,
			verdict:  VerdictBlock,
			reason:   ReasonClass + ":" + ClassDrugs,
		},
		{
			name:     "only the blocked half of an emote's classes can be the reason",
			facts:    Facts{Emote: classified(func(e *Emote) { e.Classes = []string{ClassCum, ClassViolence} })},
			settings: Settings{BlockedClasses: []string{ClassViolence}},
			verdict:  VerdictBlock,
			reason:   ReasonClass + ":" + ClassViolence,
		},
		{
			name:     "classes blocked but not present change nothing",
			facts:    Facts{Emote: classified(func(e *Emote) { e.Classes = []string{ClassDrugs} })},
			settings: Settings{BlockedClasses: []string{ClassHate, ClassSexual}},
			verdict:  VerdictAllow,
			reason:   ReasonAuto,
		},
		{
			name:     "a deleted emote is blocked",
			facts:    Facts{Emote: classified(func(e *Emote) { e.Deleted = true })},
			settings: blockAll,
			verdict:  VerdictBlock,
			reason:   ReasonDeleted,
		},
		{
			name:     "unlisted is default-deny",
			facts:    Facts{Emote: classified(func(e *Emote) { e.Listed = false })},
			settings: blockAll,
			verdict:  VerdictBlock,
			reason:   ReasonUnlisted,
		},
		{
			name:     "clean classified emote is allowed",
			facts:    Facts{Emote: classified(nil)},
			settings: blockAll,
			verdict:  VerdictAllow,
			reason:   ReasonAuto,
		},
		{
			name:     "emote never seen is unknown",
			facts:    Facts{},
			settings: blockAll,
			verdict:  VerdictUnknown,
			reason:   ReasonUnclassified,
		},
		{
			name:     "metadata without a classification is unknown",
			facts:    Facts{Emote: &Emote{ID: "e1", Name: "forsenCream", Listed: true}},
			settings: blockAll,
			verdict:  VerdictUnknown,
			reason:   ReasonUnclassified,
		},
		{
			name:     "an unlisted emote that was never classified stays unknown",
			facts:    Facts{Emote: &Emote{ID: "e1", Listed: false}},
			settings: blockAll,
			verdict:  VerdictUnknown,
			reason:   ReasonUnclassified,
		},
		{
			name:     "human lists still decide for an unclassified emote",
			facts:    Facts{Emote: &Emote{ID: "e1"}, GlobalBan: true},
			settings: blockAll,
			verdict:  VerdictBlock,
			reason:   ReasonGlobalBan,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Decide(tt.facts, tt.settings)
			assert.Equal(t, tt.verdict, got.Verdict)
			assert.Equal(t, tt.reason, got.Reason)
		})
	}
}

// Every class must be blockable on its own, so a taxonomy addition that forgets
// the verdict chain fails here instead of silently allowing.
func TestDecideBlocksEachClassIndividually(t *testing.T) {
	for _, class := range ContentClasses {
		t.Run(class, func(t *testing.T) {
			facts := Facts{Emote: classified(func(e *Emote) { e.Classes = []string{class} })}

			got := Decide(facts, Settings{BlockedClasses: []string{class}})
			assert.Equal(t, VerdictBlock, got.Verdict)
			assert.Equal(t, ReasonClass+":"+class, got.Reason)

			allowed := Decide(facts, Settings{BlockedClasses: []string{}})
			assert.Equal(t, VerdictAllow, allowed.Verdict, "nothing blocked means nothing blocks")
		})
	}
}

func TestDefaultBlockedClassesCoversTheWholeTaxonomy(t *testing.T) {
	assert.ElementsMatch(t, ContentClasses, DefaultBlockedClasses(),
		"an unconfigured channel must block every class the classifier can assign")
}

func TestFilterContentClasses(t *testing.T) {
	assert.Equal(t, []string{ClassCum, ClassSexual},
		FilterContentClasses([]string{"SEXUAL", " cum ", "cum", "nsfw", "", "spoilers"}),
		"unknown names are dropped, known ones deduplicated and sorted")
	assert.Empty(t, FilterContentClasses(nil))
}

func TestEmoteStale(t *testing.T) {
	now := time.Now()

	assert.True(t, (&Emote{ClassifiedAt: &now, ClassifierVersion: 1}).Stale(2))
	assert.False(t, (&Emote{ClassifiedAt: &now, ClassifierVersion: 2}).Stale(2))
	assert.False(t, (&Emote{ClassifierVersion: 0}).Stale(2), "never classified is not stale, it is unjudged")
	assert.False(t, (*Emote)(nil).Stale(2))
}
