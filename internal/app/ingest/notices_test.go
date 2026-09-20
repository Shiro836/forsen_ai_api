package ingest

import (
	"testing"

	"app/db"

	gempir "github.com/gempir/go-twitch-irc/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Every line is as an anonymous connection received it in a public chat on
// 2026-09-19 or 2026-09-20. Only names, words, the channel and the two
// free-text tags (system-msg, sub-plan-name) are replaced.
const (
	subNotice      = `@badge-info=subscriber/1;badges=subscriber/0,premium/1;color=#9ACD32;display-name=Some_Viewer;emotes=;flags=;id=5c2588c3-5ca7-4cad-a176-1912c99933b8;login=some_viewer;mod=0;msg-id=sub;msg-param-cumulative-months=1;msg-param-months=0;msg-param-multimonth-duration=1;msg-param-multimonth-tenure=0;msg-param-should-share-streak=0;msg-param-sub-plan-name=x;msg-param-sub-plan=Prime;msg-param-was-gifted=false;room-id=39276140;subscriber=1;system-msg=x;tmi-sent-ts=1789849235309;user-id=424549349;user-type=;vip=0 :tmi.twitch.tv USERNOTICE #streamer`
	resubNotice    = `@badge-info=subscriber/60;badges=subscriber/60,glitchcon2020/1;color=#D2691E;display-name=Some_Viewer;emotes=;flags=;id=68b7c055-2b27-42c7-84b5-6c2cd3e90dd1;login=some_viewer;mod=0;msg-id=resub;msg-param-cumulative-months=60;msg-param-months=0;msg-param-multimonth-duration=24;msg-param-multimonth-tenure=23;msg-param-should-share-streak=0;msg-param-sub-plan-name=x;msg-param-sub-plan=1000;msg-param-was-gifted=false;room-id=44445592;subscriber=1;system-msg=x;tmi-sent-ts=1789849288043;user-id=137578120;user-type=;vip=0 :tmi.twitch.tv USERNOTICE #streamer :hope you're doing fine`
	primeResub     = `@badge-info=subscriber/6;badges=subscriber/6,twitch-recap-2024/1;color=#00FF7F;display-name=Some_Viewer;emotes=;flags=;id=931a0a3b-ca63-4a3d-a10d-d6fc7e73cc90;login=some_viewer;mod=0;msg-id=resub;msg-param-cumulative-months=6;msg-param-months=0;msg-param-multimonth-duration=1;msg-param-multimonth-tenure=0;msg-param-should-share-streak=1;msg-param-streak-months=1;msg-param-sub-plan-name=x;msg-param-sub-plan=Prime;msg-param-was-gifted=false;room-id=48878319;subscriber=1;system-msg=x;tmi-sent-ts=1789849291459;user-id=601959373;user-type=;vip=0 :tmi.twitch.tv USERNOTICE #streamer`
	mysteryGift    = `@badge-info=subscriber/123;badges=subscriber/120,premium/1;color=#FF0000;display-name=some_gifter;emotes=;flags=;id=6d412482-f5d5-43ed-9a64-4cc2e2a82132;login=some_gifter;mod=0;msg-id=submysterygift;msg-param-community-gift-id=17694935589118822615;msg-param-mass-gift-count=5;msg-param-origin-id=17694935589118822615;msg-param-sender-count=32;msg-param-sub-plan=1000;room-id=26490481;subscriber=1;system-msg=x;tmi-sent-ts=1789849399856;user-id=42467504;user-type=;vip=0 :tmi.twitch.tv USERNOTICE #streamer`
	giftRecipient  = `@badge-info=subscriber/123;badges=subscriber/120,sub-gifter/25;color=#FF0000;display-name=some_gifter;emotes=;flags=;id=b33cbe0c-7c70-4e8e-8b75-bfdd98a0d2d2;login=some_gifter;mod=0;msg-id=subgift;msg-param-community-gift-id=17694935589118822615;msg-param-gift-months=1;msg-param-months=72;msg-param-origin-id=17694935589118822615;msg-param-recipient-display-name=some_viewer;msg-param-recipient-id=17662106;msg-param-recipient-user-name=some_viewer;msg-param-sender-count=0;msg-param-sub-plan-name=x;msg-param-sub-plan=1000;room-id=26490481;subscriber=1;system-msg=x;tmi-sent-ts=1789849400894;user-id=42467504;user-type=;vip=0 :tmi.twitch.tv USERNOTICE #streamer`
	standaloneGift = `@badge-info=subscriber/86;badges=broadcaster/1,subscriber/3072,partner/1;color=#DAA520;display-name=some_gifter;emotes=;flags=;id=f70d0ea6-16c6-4c17-addb-a8da1af874ed;login=some_gifter;mod=0;msg-id=subgift;msg-param-gift-months=1;msg-param-months=3;msg-param-origin-id=3579205679884335276;msg-param-recipient-display-name=some_viewer;msg-param-recipient-id=1496027923;msg-param-recipient-user-name=some_viewer;msg-param-sender-count=0;msg-param-sub-plan-name=x;msg-param-sub-plan=1000;room-id=162957501;subscriber=1;system-msg=x;tmi-sent-ts=1789900513749;user-id=162957501;user-type=;vip=0 :tmi.twitch.tv USERNOTICE #streamer`
	anonymousGift  = `@badge-info=;badges=;color=;display-name=AnAnonymousGifter;emotes=;flags=;id=fdfa1527-3499-4881-b8e2-efd7f463b44d;login=ananonymousgifter;mod=0;msg-id=subgift;msg-param-fun-string=FunStringThree;msg-param-gift-months=1;msg-param-months=6;msg-param-origin-id=9320595997383922116;msg-param-recipient-display-name=some_viewer;msg-param-recipient-id=123716893;msg-param-recipient-user-name=some_viewer;msg-param-sub-plan-name=x;msg-param-sub-plan=1000;room-id=36196174;subscriber=0;system-msg=x;tmi-sent-ts=1789900613956;user-id=274598607;user-type=;vip=0 :tmi.twitch.tv USERNOTICE #streamer`
	raidNotice     = `@badge-info=;badges=partner/1;color=#00FF7F;display-name=Some_Raider;emotes=;flags=;id=49c7fe4b-1cd7-4afe-a727-c492331657d6;login=some_raider;mod=0;msg-id=raid;msg-param-displayName=Some_Raider;msg-param-login=some_raider;msg-param-profileImageURL=x;msg-param-viewerCount=91;room-id=209005581;subscriber=0;system-msg=x;tmi-sent-ts=1789900530893;user-id=150480078;user-type=;vip=0 :tmi.twitch.tv USERNOTICE #streamer`
	streakNotice   = `@badge-info=subscriber/46;badges=subscriber/42,pichu/1;color=#FF0000;display-name=Some_Viewer;emotes=;flags=;id=9807c606-e5b3-4dd0-a066-4ea5fdbadff3;login=some_viewer;mod=0;msg-id=viewermilestone;msg-param-category=watch-streak;msg-param-copoReward=450;msg-param-id=71616713-26ae-4427-bb6c-f37c2480a3b0;msg-param-value=25;room-id=39276140;subscriber=1;system-msg=x;tmi-sent-ts=1789849221358;user-id=125629679;user-type=;vip=0 :tmi.twitch.tv USERNOTICE #streamer :hello`

	cheerMessage = `@badge-info=;badges=;bits=200;color=#0000FF;display-name=Some_Viewer;emotes=;flags=;id=0b91cd7d-01f2-4f3e-b23a-6d38e55df643;mod=0;room-id=48878319;subscriber=0;tmi-sent-ts=1789849219807;turbo=0;user-id=202021370;user-type= :some_viewer!some_viewer@some_viewer.tmi.twitch.tv PRIVMSG #streamer :Cheer200 lo de verte en stream`
	// Gigantify an Emote, a Power-up priced in bits.
	powerUpMessage = `@badge-info=subscriber/67;badges=subscriber/24,warlord/1;color=#DAA520;display-name=Some_Viewer;emote-only=1;emotes=emotesv2_61323e440bb14aeda42fe6a310f5e58f:0-17;first-msg=0;flags=;id=91f1439d-765d-49e2-b801-17508721b005;mod=0;msg-id=gigantified-emote-message;returning-chatter=0;room-id=506919714;subscriber=1;tmi-sent-ts=1789900535146;turbo=0;user-id=651109594;user-type= :some_viewer!some_viewer@some_viewer.tmi.twitch.tv PRIVMSG #streamer :someEmote`
)

func notice(t *testing.T, raw string) gempir.UserNoticeMessage {
	t.Helper()
	msg, ok := gempir.ParseMessage(raw).(*gempir.UserNoticeMessage)
	require.True(t, ok)
	return *msg
}

func chatMessage(t *testing.T, raw string) gempir.PrivateMessage {
	t.Helper()
	msg, ok := gempir.ParseMessage(raw).(*gempir.PrivateMessage)
	require.True(t, ok)
	return *msg
}

func TestNoticeArrival(t *testing.T) {
	viewer := func(id int, text string, event db.EventMeta) db.TwitchMessage {
		return db.TwitchMessage{TwitchLogin: "some_viewer", TwitchUserID: id, Message: text, Event: &event}
	}
	gifter := func(id int, event db.EventMeta) db.TwitchMessage {
		return db.TwitchMessage{TwitchLogin: "some_gifter", TwitchUserID: id, Event: &event}
	}

	cases := []struct {
		name string
		raw  string
		want arrival
	}{
		{"new sub", subNotice, arrival{
			msg:      viewer(424549349, "", db.EventMeta{Kind: db.EventKindSub, Tier: 1}),
			uniqueID: "5c2588c3-5ca7-4cad-a176-1912c99933b8",
			pairAs:   pairAsSub,
		}},
		{"resub with the viewer's words", resubNotice, arrival{
			msg:      viewer(137578120, "hope you're doing fine", db.EventMeta{Kind: db.EventKindResub, Tier: 1, Months: 60}),
			uniqueID: "68b7c055-2b27-42c7-84b5-6c2cd3e90dd1",
			pairAs:   pairAsResub,
		}},
		{"prime resub without words", primeResub, arrival{
			msg:      viewer(601959373, "", db.EventMeta{Kind: db.EventKindResub, Tier: 1, Months: 6}),
			uniqueID: "931a0a3b-ca63-4a3d-a10d-d6fc7e73cc90",
			pairAs:   pairAsResub,
		}},
		{"community gift", mysteryGift, arrival{
			msg:      gifter(42467504, db.EventMeta{Kind: db.EventKindGiftSubs, Tier: 1, GiftCount: 5}),
			uniqueID: "6d412482-f5d5-43ed-9a64-4cc2e2a82132",
			pairAs:   "gift:5",
		}},
		{"gift to one viewer", standaloneGift, arrival{
			msg:      gifter(162957501, db.EventMeta{Kind: db.EventKindGiftSubs, Tier: 1, GiftCount: 1}),
			uniqueID: "f70d0ea6-16c6-4c17-addb-a8da1af874ed",
			pairAs:   "gift:1",
		}},
		{"anonymous gift, which the event feed sends with no gifter at all", anonymousGift, arrival{
			msg:      db.TwitchMessage{TwitchLogin: anonymousLogin, Event: &db.EventMeta{Kind: db.EventKindGiftSubs, Tier: 1, GiftCount: 1}},
			uniqueID: "fdfa1527-3499-4881-b8e2-efd7f463b44d",
			pairAs:   "gift:1",
		}},
		{"raid", raidNotice, arrival{
			msg:      db.TwitchMessage{TwitchLogin: "some_raider", TwitchUserID: 150480078, Event: &db.EventMeta{Kind: db.EventKindRaid, Viewers: 91}},
			uniqueID: "49c7fe4b-1cd7-4afe-a727-c492331657d6",
			pairAs:   pairAsRaid,
		}},
		{"watch streak", streakNotice, arrival{
			msg:      viewer(125629679, "hello", db.EventMeta{Kind: db.EventKindStreak, Streak: 25}),
			uniqueID: "9807c606-e5b3-4dd0-a066-4ea5fdbadff3",
		}},
	}
	for _, tc := range cases {
		in, ok := noticeArrival(notice(t, tc.raw))
		require.True(t, ok, tc.name)
		assert.Equal(t, tc.want, in, tc.name)
	}

	_, ok := noticeArrival(notice(t, giftRecipient))
	assert.False(t, ok, "a community gift is one message, not one per recipient")
}

func TestOnlyACheerIsACheer(t *testing.T) {
	bits, cheered := cheerBits(chatMessage(t, cheerMessage))
	assert.True(t, cheered)
	assert.Equal(t, 200, bits)

	powerUp := chatMessage(t, powerUpMessage)
	assert.Zero(t, powerUp.Bits, "as received: a Power-up bought with bits carries no bits tag")
	_, cheered = cheerBits(powerUp)
	assert.False(t, cheered, "a Power-up is not a cheer")

	// No such message was ever seen; this one is made up to pin the rule down.
	powerUp.Bits = 30
	_, cheered = cheerBits(powerUp)
	assert.False(t, cheered, "a bits tag would still not make a Power-up a cheer")

	shared := chatMessage(t, cheerMessage)
	shared.Tags["source-room-id"] = "1"
	_, cheered = cheerBits(shared)
	assert.False(t, cheered, "a cheer in another channel's shared chat is not ours")
}

func TestNoticeFromAnotherChannelIsIgnored(t *testing.T) {
	msg := notice(t, streakNotice)
	msg.Tags["source-room-id"] = "1"
	_, ok := noticeArrival(msg)
	assert.False(t, ok)

	msg.Tags["source-room-id"] = msg.Tags["room-id"]
	_, ok = noticeArrival(msg)
	assert.True(t, ok)
}
