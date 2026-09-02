package emoteservice

import (
	"slices"
	"strings"
	"time"
)

type Verdict string

const (
	VerdictAllow Verdict = "allow"
	VerdictBlock Verdict = "block"
	// VerdictUnknown means "never judged" — the overlay leaves the word as plain
	// text, which is the behaviour it had before emote rendering existed.
	VerdictUnknown Verdict = "unknown"
)

// ProviderSevenTV is the only emote provider implemented. It is threaded through
// every emote key anyway: a live channel was measured to have no 7TV set at all
// and to run entirely on BTTV, so a second provider is coming.
const ProviderSevenTV = "7tv"

// NormalizeProvider defaults an unset provider to the only one implemented.
func NormalizeProvider(p string) string {
	if p == "" {
		return ProviderSevenTV
	}
	return p
}

// GlobalScope is the streamer_id carried by override rows that apply to every
// channel. Real ids are users.id from the main database, which is uuidv7, so the
// nil uuid can never collide with one.
const GlobalScope = "00000000-0000-0000-0000-000000000000"

// The content classes the vision model may assign. They are a fixed vocabulary
// in the prompt and in the UI, but text[] in storage, so adding one needs no
// migration beyond folding it into the settings rows that were written before
// it existed.
const (
	ClassCum      = "cum"
	ClassSexual   = "sexual"
	ClassViolence = "violence"
	ClassHate     = "hate"
	ClassFlashing = "flashing"
	ClassDrugs    = "drugs"
	ClassGambling = "gambling"
	// ClassFluids is every bodily fluid Twitch's ToS covers except semen, which
	// keeps its own class: the cream trope and toilet humour are different
	// cultures and channels toggle them separately. Blood stays under violence.
	ClassFluids = "fluids"
)

// ContentClasses is the whole vocabulary, in the order toggles are rendered.
var ContentClasses = []string{ClassCum, ClassSexual, ClassViolence, ClassHate, ClassFlashing, ClassDrugs, ClassGambling, ClassFluids}

// IsContentClass rejects anything the model invented. An unknown class is
// dropped rather than stored, so no verdict can hinge on a hallucinated name.
func IsContentClass(class string) bool {
	return slices.Contains(ContentClasses, class)
}

// FilterContentClasses keeps the known classes of a list, deduplicated and
// sorted so two equal sets compare and render identically.
func FilterContentClasses(classes []string) []string {
	out := make([]string, 0, len(classes))
	for _, class := range classes {
		class = strings.ToLower(strings.TrimSpace(class))
		if IsContentClass(class) && !slices.Contains(out, class) {
			out = append(out, class)
		}
	}
	slices.Sort(out)

	return out
}

// Emote is one emote's cached 7TV metadata and classification.
type Emote struct {
	Provider    string   `json:"provider"`
	ID          string   `json:"emote_id"`
	Name        string   `json:"name"`
	Listed      bool     `json:"listed"`
	Animated    bool     `json:"animated"`
	Deleted     bool     `json:"deleted"`
	Flags       int      `json:"flags"`
	Tags        []string `json:"tags,omitempty"`
	Description string   `json:"description"`
	// Classes is what the vision model found in the emote, not what any channel
	// blocks; the two meet in Decide.
	Classes           []string   `json:"classes"`
	Model             string     `json:"model,omitempty"`
	ClassifierVersion int        `json:"classifier_version"`
	ClassifiedAt      *time.Time `json:"classified_at,omitempty"`
	OriginalKey       string     `json:"original_key,omitempty"`
	GridKey           string     `json:"grid_key,omitempty"`

	// The measured flash rates behind the flashing class. They are kept beside the
	// class so a streamer asking why an emote was blocked gets a number rather
	// than a model's opinion.
	FlashHz         float64    `json:"flash_hz"`
	FlashHzMax      float64    `json:"flash_hz_max"`
	RedFlashHz      float64    `json:"red_flash_hz"`
	FlashMeasuredAt *time.Time `json:"flash_measured_at,omitempty"`

	// HasImageEmbedding lets the worker skip re-embedding a grid it has already
	// embedded; the vector itself is never read out of the database.
	HasImageEmbedding bool `json:"-"`
}

func (e *Emote) Classified() bool { return e != nil && e.ClassifiedAt != nil }

// Stale reports whether an emote was judged under an older taxonomy and needs
// classifying again.
func (e *Emote) Stale(currentVersion int) bool {
	return e.Classified() && e.ClassifierVersion < currentVersion
}

// Facts is everything the precedence chain needs about one emote in one
// channel. The store gathers it for a whole batch so a verdict lookup is a
// fixed number of round trips.
type Facts struct {
	Emote           *Emote
	StreamerBan     bool
	StreamerApprove bool
	GlobalBan       bool
	GlobalApprove   bool
	RuleID          string
}

// Settings is a channel's effective auto-verdict configuration: BlockedClasses
// is always the set actually in force, and Inherited says whether it came from
// the platform default rather than from a choice this channel made.
type Settings struct {
	StreamerID     string   `json:"streamer_id"`
	BlockedClasses []string `json:"blocked_classes"`
	Inherited      bool     `json:"inherited"`
}

// DefaultBlockedClasses is the fail-safe the platform row is seeded with and the
// fallback if that row is ever missing.
func DefaultBlockedClasses() []string {
	return slices.Clone(ContentClasses)
}

type Decision struct {
	EmoteID string  `json:"emote_id"`
	Verdict Verdict `json:"verdict"`
	Reason  string  `json:"reason"`
}

const (
	ReasonStreamerBan     = "streamer_ban"
	ReasonStreamerApprove = "streamer_approve"
	ReasonGlobalBan       = "global_ban"
	ReasonGlobalApprove   = "global_approve"
	ReasonRule            = "rule"
	// ReasonClass carries the offending class after a colon.
	ReasonClass        = "class"
	ReasonDeleted      = "deleted"
	ReasonUnlisted     = "unlisted"
	ReasonAuto         = "auto"
	ReasonUnclassified = "unclassified"
)

// firstBlockedClass returns the alphabetically first class an emote carries that
// the channel blocks. Alphabetical rather than "as classified" so the reason a
// moderator sees is the same one every time.
func firstBlockedClass(classes, blocked []string) string {
	hit := ""
	for _, class := range classes {
		if !slices.Contains(blocked, class) {
			continue
		}
		if hit == "" || class < hit {
			hit = class
		}
	}

	return hit
}

// Decide walks the moderation precedence chain, first match wins. It is pure so
// the ordering can be tested without a database.
func Decide(f Facts, s Settings) Decision {
	var emoteID string
	if f.Emote != nil {
		emoteID = f.Emote.ID
	}
	d := func(v Verdict, reason string) Decision {
		return Decision{EmoteID: emoteID, Verdict: v, Reason: reason}
	}

	switch {
	case f.StreamerBan:
		return d(VerdictBlock, ReasonStreamerBan)
	case f.StreamerApprove:
		return d(VerdictAllow, ReasonStreamerApprove)
	case f.GlobalBan:
		return d(VerdictBlock, ReasonGlobalBan)
	case f.GlobalApprove:
		return d(VerdictAllow, ReasonGlobalApprove)
	case f.RuleID != "":
		return d(VerdictBlock, ReasonRule+":"+f.RuleID)
	}

	if !f.Emote.Classified() {
		return d(VerdictUnknown, ReasonUnclassified)
	}

	if class := firstBlockedClass(f.Emote.Classes, s.BlockedClasses); class != "" {
		return d(VerdictBlock, ReasonClass+":"+class)
	}

	switch {
	case f.Emote.Deleted:
		return d(VerdictBlock, ReasonDeleted)
	case !f.Emote.Listed:
		return d(VerdictBlock, ReasonUnlisted)
	}
	return d(VerdictAllow, ReasonAuto)
}
