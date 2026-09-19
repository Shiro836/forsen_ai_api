package db

import (
	"reflect"
	"testing"

	"github.com/google/uuid"
)

func TestPlayOrder(t *testing.T) {
	full := [][]MsgClass{{MsgClassFollow}, {MsgClassStreak}, {MsgClassReward}, {MsgClassBits, MsgClassDonation}, {MsgClassRaid}, {MsgClassSub}}

	cases := []struct {
		name   string
		stored [][]MsgClass
		want   [][]MsgClass
	}{
		{"nothing stored", nil, defaultPlayGroups},
		{"every lane ranked", full, full},
		{
			"lanes added since it was stored go last",
			[][]MsgClass{{MsgClassReward}, {MsgClassBits, MsgClassDonation}},
			[][]MsgClass{{MsgClassReward}, {MsgClassBits, MsgClassDonation}, {MsgClassSub}, {MsgClassRaid}, {MsgClassStreak}, {MsgClassFollow}},
		},
		{"unpaid lane in a group", [][]MsgClass{{MsgClassReward, MsgClassBits}, {MsgClassDonation}}, defaultPlayGroups},
		{"lane repeated", [][]MsgClass{{MsgClassReward}, {MsgClassBits}, {MsgClassDonation}, {MsgClassBits}}, defaultPlayGroups},
		{"chat ranked", [][]MsgClass{{MsgClassChat}, {MsgClassReward}, {MsgClassBits}, {MsgClassDonation}}, defaultPlayGroups},
		{"empty group", [][]MsgClass{{}, {MsgClassReward}, {MsgClassBits}, {MsgClassDonation}}, defaultPlayGroups},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := (&UserSettings{PlayGroups: tc.stored}).PlayOrder()
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("PlayOrder() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPlayOrderDoesNotAliasTheDefault(t *testing.T) {
	order := (&UserSettings{}).PlayOrder()
	order[0][0] = MsgClassChat
	if defaultPlayGroups[0][0] == MsgClassChat {
		t.Fatal("mutating a returned order changed the default")
	}
}

func TestLaneEnabled(t *testing.T) {
	settings := &UserSettings{}
	for class, want := range map[MsgClass]bool{
		MsgClassDonation: true, MsgClassBits: true, MsgClassReward: true,
		MsgClassSub: false, MsgClassRaid: false, MsgClassStreak: false, MsgClassFollow: false, MsgClassChat: false,
	} {
		if got := settings.LaneEnabled(class); got != want {
			t.Errorf("LaneEnabled(%s) = %v by default, want %v", class, got, want)
		}
	}

	settings.SetLaneEnabled(MsgClassSub, true)
	settings.SetLaneEnabled(MsgClassReward, false)
	settings.SetLaneEnabled(MsgClassChat, true)
	if !settings.LaneEnabled(MsgClassSub) || settings.LaneEnabled(MsgClassReward) {
		t.Fatalf("switched lanes not remembered: %v", settings.LanesEnabled)
	}
	if !settings.IngestAllMessages {
		t.Fatal("chat lane must be the chat TTS setting itself")
	}

	settings.SetLaneEnabled(MsgClassSub, false)
	settings.SetLaneEnabled(MsgClassReward, true)
	if len(settings.LanesEnabled) != 0 {
		t.Fatalf("defaults must not be stored: %v", settings.LanesEnabled)
	}
}

func TestSetPlayOrder(t *testing.T) {
	settings := &UserSettings{}

	reordered := [][]MsgClass{{MsgClassReward}, {MsgClassDonation, MsgClassBits}, {MsgClassSub}, {MsgClassRaid}, {MsgClassStreak}, {MsgClassFollow}}
	if err := settings.SetPlayOrder(reordered); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(settings.PlayOrder(), reordered) {
		t.Fatalf("PlayOrder() = %v", settings.PlayOrder())
	}

	if err := settings.SetPlayOrder([][]MsgClass{{MsgClassReward}, {MsgClassBits}}); err == nil {
		t.Fatal("an order that leaves lanes out was accepted")
	}

	if err := settings.SetPlayOrder(defaultPlayGroups); err != nil {
		t.Fatal(err)
	}
	if settings.PlayGroups != nil {
		t.Fatalf("the default order must not be stored: %v", settings.PlayGroups)
	}
}

func TestSetEventAction(t *testing.T) {
	settings := &UserSettings{}

	if err := settings.SetEventAction(MsgClassSub, EventAction{RewardType: TwitchRewardAI}); err == nil {
		t.Fatal("a character action without its character was accepted")
	}

	card := uuid.New()
	if err := settings.SetEventAction(MsgClassSub, EventAction{RewardType: TwitchRewardAI, CardID: &card}); err != nil {
		t.Fatal(err)
	}
	if got := settings.EventAction(MsgClassSub); got.RewardType != TwitchRewardAI || *got.CardID != card {
		t.Fatalf("EventAction = %+v", got)
	}

	if err := settings.SetEventAction(MsgClassSub, EventAction{RewardType: TwitchRewardUniversalTTS, CardID: &card}); err != nil {
		t.Fatal(err)
	}
	if len(settings.EventActions) != 0 {
		t.Fatalf("the default action must not be stored: %v", settings.EventActions)
	}
}

func TestEventLine(t *testing.T) {
	settings := &UserSettings{}
	if got := settings.EventLine(EventLineRaid); got != EventLineRaid.Default() {
		t.Fatalf("EventLine = %q, want the default", got)
	}

	if err := settings.SetEventLine(EventLineRaid, "  {user}   brought {viewers} raiders "); err != nil {
		t.Fatal(err)
	}
	if got := settings.EventLine(EventLineRaid); got != "{user} brought {viewers} raiders" {
		t.Fatalf("EventLine = %q", got)
	}

	if err := settings.SetEventLine(EventLineRaid, "{user} for {months} months"); err == nil {
		t.Fatal("a placeholder the raid cannot fill was accepted")
	}
	if got := settings.EventLine(EventLineRaid); got != "{user} brought {viewers} raiders" {
		t.Fatalf("a refused line replaced the stored one: %q", got)
	}

	for _, clearing := range []string{"", EventLineRaid.Default()} {
		if err := settings.SetEventLine(EventLineRaid, clearing); err != nil {
			t.Fatal(err)
		}
		if _, stored := settings.EventLines[EventLineRaid]; stored {
			t.Fatalf("%q should clear the stored line", clearing)
		}
	}
}

func TestSpokenText(t *testing.T) {
	settings := &UserSettings{}
	if err := settings.SetEventLine(EventLineGift, "{user} dropped {count} tier {tier} subs"); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		msg  TwitchMessage
		want string
	}{
		{"chat", TwitchMessage{TwitchLogin: "a", Message: "hi"}, "hi"},
		{"redeem", TwitchMessage{TwitchLogin: "a", Message: "hi", RewardID: "r", Event: &EventMeta{Kind: EventKindPointsRedeem}}, "hi"},
		{"the viewer's words win", TwitchMessage{TwitchLogin: "a", Message: "12 months!", Event: &EventMeta{Kind: EventKindResub, Months: 12}}, "12 months!"},
		{"default line", TwitchMessage{TwitchLogin: "a", Event: &EventMeta{Kind: EventKindResub, Months: 12, Tier: 1}}, "a subscribed for 12 months"},
		{"the streamer's line", TwitchMessage{TwitchLogin: "a", Event: &EventMeta{Kind: EventKindGiftSubs, GiftCount: 5, Tier: 3}}, "a dropped 5 tier 3 subs"},
		{"raid", TwitchMessage{TwitchLogin: "a", Event: &EventMeta{Kind: EventKindRaid, Viewers: 40}}, "a is raiding with 40 viewers"},
		{"streak", TwitchMessage{TwitchLogin: "a", Event: &EventMeta{Kind: EventKindStreak, Streak: 7}}, "a watched 7 streams in a row"},
		{"follow", TwitchMessage{TwitchLogin: "a", Event: &EventMeta{Kind: EventKindFollow}}, "a followed"},
		{"a value the event lacks renders empty", TwitchMessage{TwitchLogin: "a", Event: &EventMeta{Kind: EventKindGiftSubs, GiftCount: 5}}, "a dropped 5 tier  subs"},
	}
	for _, tc := range cases {
		if got := settings.SpokenText(&tc.msg); got != tc.want {
			t.Errorf("%s: SpokenText = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestYieldingLanes(t *testing.T) {
	if got := (&UserSettings{}).YieldingLanes(); !reflect.DeepEqual(got, []MsgClass{MsgClassFollow}) {
		t.Fatalf("YieldingLanes = %v, want follows", got)
	}
	if got := (&UserSettings{FollowsStay: true}).YieldingLanes(); len(got) != 0 {
		t.Fatalf("YieldingLanes = %v, want none", got)
	}
}
