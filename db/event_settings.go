package db

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

var lanesOnByDefault = []MsgClass{MsgClassDonation, MsgClassBits, MsgClassReward}

func (s *UserSettings) LaneEnabled(class MsgClass) bool {
	if class == MsgClassChat {
		return s.IngestAllMessages
	}
	if enabled, ok := s.LanesEnabled[class]; ok {
		return enabled
	}
	return slices.Contains(lanesOnByDefault, class)
}

// Setters below store only what differs from the default, so a streamer who
// never chose otherwise follows the default when it changes.
func (s *UserSettings) SetLaneEnabled(class MsgClass, enabled bool) {
	if class == MsgClassChat {
		s.IngestAllMessages = enabled
		return
	}
	if enabled == slices.Contains(lanesOnByDefault, class) {
		delete(s.LanesEnabled, class)
		return
	}
	if s.LanesEnabled == nil {
		s.LanesEnabled = make(map[MsgClass]bool)
	}
	s.LanesEnabled[class] = enabled
}

func (s *UserSettings) SetPlayOrder(order [][]MsgClass) error {
	if !validPlayGroups(order) || len(slices.Concat(order...)) != len(slices.Concat(defaultPlayGroups...)) {
		return fmt.Errorf("invalid play order")
	}
	if slices.EqualFunc(order, defaultPlayGroups, func(a, b []MsgClass) bool { return slices.Equal(a, b) }) {
		s.PlayGroups = nil
		return nil
	}
	s.PlayGroups = order
	return nil
}

func (s *UserSettings) SetEventAction(class MsgClass, action EventAction) error {
	if action.RewardType == TwitchRewardUniversalTTS {
		delete(s.EventActions, class)
		return nil
	}
	if !action.Complete() {
		return fmt.Errorf("pick a character")
	}
	if s.EventActions == nil {
		s.EventActions = make(map[MsgClass]*EventAction)
	}
	s.EventActions[class] = &action
	return nil
}

// EnabledPlayOrder leaves out the lanes that are off: their messages must not
// outrank or interrupt anything on the way to being dropped.
func (s *UserSettings) EnabledPlayOrder() [][]MsgClass {
	var order [][]MsgClass
	for _, group := range s.PlayOrder() {
		group = slices.DeleteFunc(group, func(class MsgClass) bool { return !s.LaneEnabled(class) })
		if len(group) > 0 {
			order = append(order, group)
		}
	}
	return order
}

// EventLine is what TTS says for an event whose viewer typed nothing.
type EventLine string

const (
	EventLineSub    EventLine = "sub"
	EventLineResub  EventLine = "resub"
	EventLineGift   EventLine = "gift"
	EventLineRaid   EventLine = "raid"
	EventLineStreak EventLine = "streak"
	EventLineFollow EventLine = "follow"
)

type eventLineSpec struct {
	text         string
	placeholders []string
}

var eventLineSpecs = map[EventLine]eventLineSpec{
	EventLineSub:    {"{user} subscribed", []string{"user", "tier"}},
	EventLineResub:  {"{user} subscribed for {months} months", []string{"user", "months", "tier"}},
	EventLineGift:   {"{user} gifted {count} subs", []string{"user", "count", "tier"}},
	EventLineRaid:   {"{user} is raiding with {viewers} viewers", []string{"user", "viewers"}},
	EventLineStreak: {"{user} watched {streak} streams in a row", []string{"user", "streak"}},
	EventLineFollow: {"{user} followed", []string{"user"}},
}

const EventLineMaxLen = 200

var eventLinePlaceholder = regexp.MustCompile(`\{([^{}]*)\}`)

func (l EventLine) Valid() bool {
	_, ok := eventLineSpecs[l]
	return ok
}

func (l EventLine) Default() string {
	return eventLineSpecs[l].text
}

func (l EventLine) Placeholders() []string {
	return eventLineSpecs[l].placeholders
}

func (s *UserSettings) EventLine(line EventLine) string {
	if text := s.EventLines[line]; text != "" {
		return text
	}
	return line.Default()
}

var eventKindLines = map[EventKind]EventLine{
	EventKindSub:      EventLineSub,
	EventKindResub:    EventLineResub,
	EventKindGiftSubs: EventLineGift,
	EventKindRaid:     EventLineRaid,
	EventKindStreak:   EventLineStreak,
	EventKindFollow:   EventLineFollow,
}

// SpokenText is the viewer's own message when they typed one, otherwise the
// streamer's line for the event. A placeholder the event has no value for
// renders empty.
func (s *UserSettings) SpokenText(msg *TwitchMessage) string {
	if msg.Message != "" || msg.Event == nil {
		return msg.Message
	}
	line, ok := eventKindLines[msg.Event.Kind]
	if !ok {
		return ""
	}

	number := func(n int) string {
		if n == 0 {
			return ""
		}
		return strconv.Itoa(n)
	}
	values := map[string]string{
		"user":    msg.TwitchLogin,
		"tier":    number(msg.Event.Tier),
		"months":  number(msg.Event.Months),
		"count":   number(msg.Event.GiftCount),
		"viewers": number(msg.Event.Viewers),
		"streak":  number(msg.Event.Streak),
	}

	return eventLinePlaceholder.ReplaceAllStringFunc(s.EventLine(line), func(placeholder string) string {
		return values[strings.Trim(placeholder, "{}")]
	})
}

// YieldingLanes are dropped from the queue and cut off mid-playback by
// anything ranked above them.
func (s *UserSettings) YieldingLanes() []MsgClass {
	if s.FollowsStay {
		return nil
	}
	return []MsgClass{MsgClassFollow}
}

// SetEventLine stores text for line; empty text or the default itself clears
// the stored one, so a later change of the default reaches the streamer.
func (s *UserSettings) SetEventLine(line EventLine, text string) error {
	text = strings.Join(strings.Fields(text), " ")

	if len([]rune(text)) > EventLineMaxLen {
		return fmt.Errorf("max %d characters", EventLineMaxLen)
	}
	for _, match := range eventLinePlaceholder.FindAllStringSubmatch(text, -1) {
		if !slices.Contains(line.Placeholders(), match[1]) {
			return fmt.Errorf("unknown {%s}", match[1])
		}
	}

	if text == "" || text == line.Default() {
		delete(s.EventLines, line)
		return nil
	}
	if s.EventLines == nil {
		s.EventLines = make(map[EventLine]string)
	}
	s.EventLines[line] = text
	return nil
}
