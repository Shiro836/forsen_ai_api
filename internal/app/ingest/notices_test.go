package ingest

import (
	"testing"

	"app/db"

	gempir "github.com/gempir/go-twitch-irc/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Lines are as an anonymous connection received them in public chats on
// 2026-09-19, with the viewers' names and words replaced.
const (
	resubNotice  = `@badge-info=subscriber/60;badges=subscriber/60,glitchcon2020/1;color=#D2691E;display-name=Some_Viewer;emotes=;flags=;id=68b7c055-2b27-42c7-84b5-6c2cd3e90dd1;login=some_viewer;mod=0;msg-id=resub;msg-param-cumulative-months=60;msg-param-months=0;msg-param-multimonth-duration=24;msg-param-multimonth-tenure=23;msg-param-should-share-streak=0;msg-param-sub-plan-name=Channel\sSubscription\s(streamer);msg-param-sub-plan=1000;msg-param-was-gifted=false;room-id=44445592;subscriber=1;system-msg=Some_Viewer\ssubscribed\sat\sTier\s1.\sThey've\ssubscribed\sfor\s60\smonths!;tmi-sent-ts=1789849288043;user-id=137578120;user-type=;vip=0 :tmi.twitch.tv USERNOTICE #streamer :hope you're doing fine`
	primeResub   = `@badge-info=subscriber/12;badges=subscriber/12;color=;display-name=Some_Viewer;emotes=;flags=;id=1b0e7f0c-7b8e-4f0a-9d53-0b0e8f0a2c11;login=some_viewer;mod=0;msg-id=resub;msg-param-cumulative-months=12;msg-param-months=0;msg-param-multimonth-duration=1;msg-param-multimonth-tenure=0;msg-param-should-share-streak=0;msg-param-sub-plan-name=Channel\sSubscription\s(streamer);msg-param-sub-plan=Prime;msg-param-was-gifted=false;room-id=44445592;subscriber=1;system-msg=Some_Viewer\ssubscribed\swith\sPrime.;tmi-sent-ts=1789849288043;user-id=137578120;user-type=;vip=0 :tmi.twitch.tv USERNOTICE #streamer`
	streakNotice = `@badge-info=subscriber/46;badges=subscriber/42,pichu/1;color=#FF0000;display-name=Some_Viewer;emotes=;flags=;id=9807c606-e5b3-4dd0-a066-4ea5fdbadff3;login=some_viewer;mod=0;msg-id=viewermilestone;msg-param-category=watch-streak;msg-param-copoReward=450;msg-param-id=71616713-26ae-4427-bb6c-f37c2480a3b0;msg-param-value=25;room-id=39276140;subscriber=1;system-msg=Some_Viewer\swatched\s25\sconsecutive\sstreams\sand\ssparked\sa\swatch\sstreak!;tmi-sent-ts=1789849221358;user-id=125629679;user-type=;vip=0 :tmi.twitch.tv USERNOTICE #streamer :hello`
	subNotice    = `@badge-info=subscriber/1;badges=subscriber/0;color=;display-name=Some_Viewer;emotes=;flags=;id=5d4a1f6e-0c55-4f0e-8a4e-7d2f6f1f6a10;login=some_viewer;mod=0;msg-id=sub;msg-param-cumulative-months=1;msg-param-months=0;msg-param-multimonth-duration=1;msg-param-multimonth-tenure=0;msg-param-should-share-streak=0;msg-param-sub-plan-name=Channel\sSubscription\s(streamer);msg-param-sub-plan=Prime;msg-param-was-gifted=false;room-id=44445592;subscriber=1;system-msg=Some_Viewer\ssubscribed\swith\sPrime.;tmi-sent-ts=1789849288043;user-id=137578120;user-type=;vip=0 :tmi.twitch.tv USERNOTICE #streamer`
)

func notice(t *testing.T, raw string) gempir.UserNoticeMessage {
	t.Helper()
	msg, ok := gempir.ParseMessage(raw).(*gempir.UserNoticeMessage)
	require.True(t, ok)
	return *msg
}

func TestNoticeArrival(t *testing.T) {
	in, ok := noticeArrival(notice(t, resubNotice))
	require.True(t, ok)
	assert.Equal(t, arrival{
		msg: db.TwitchMessage{
			TwitchLogin:  "some_viewer",
			TwitchUserID: 137578120,
			Message:      "hope you're doing fine",
			Event:        &db.EventMeta{Kind: db.EventKindResub, Tier: 1, Months: 60},
		},
		uniqueID: "68b7c055-2b27-42c7-84b5-6c2cd3e90dd1",
		pairAs:   pairAsResub,
	}, in)

	in, ok = noticeArrival(notice(t, primeResub))
	require.True(t, ok)
	assert.Empty(t, in.msg.Message)
	assert.Equal(t, &db.EventMeta{Kind: db.EventKindResub, Tier: 1, Months: 12}, in.msg.Event)

	in, ok = noticeArrival(notice(t, streakNotice))
	require.True(t, ok)
	assert.Equal(t, arrival{
		msg: db.TwitchMessage{
			TwitchLogin:  "some_viewer",
			TwitchUserID: 125629679,
			Message:      "hello",
			Event:        &db.EventMeta{Kind: db.EventKindStreak, Streak: 25},
		},
		uniqueID: "9807c606-e5b3-4dd0-a066-4ea5fdbadff3",
	}, in)

	_, ok = noticeArrival(notice(t, subNotice))
	assert.False(t, ok, "a new sub is the event feed's to deliver")
}

// Both lines are real, captured 2026-09-19 with the viewer and channel replaced.
func TestOnlyACheerIsACheer(t *testing.T) {
	const (
		cheer   = `@badge-info=;badges=;bits=200;color=#0000FF;display-name=Some_Viewer;emotes=;flags=;id=0b91cd7d-01f2-4f3e-b23a-6d38e55df643;mod=0;room-id=48878319;subscriber=0;tmi-sent-ts=1789849219807;turbo=0;user-id=202021370;user-type= :some_viewer!some_viewer@some_viewer.tmi.twitch.tv PRIVMSG #streamer :Cheer200 lo de verte en stream`
		powerUp = `@badge-info=;badges=;color=;display-name=Some_Viewer;emotes=;first-msg=0;flags=;id=8a7b6c5d-0e1f-4a2b-9c3d-4e5f6a7b8c9d;mod=0;msg-id=highlighted-message;room-id=175831187;subscriber=0;tmi-sent-ts=1789850000000;turbo=0;user-id=202021370;user-type= :some_viewer!some_viewer@some_viewer.tmi.twitch.tv PRIVMSG #streamer :look at me`
	)

	parse := func(raw string) gempir.PrivateMessage {
		msg, ok := gempir.ParseMessage(raw).(*gempir.PrivateMessage)
		require.True(t, ok)
		return *msg
	}

	bits, cheered := cheerBits(parse(cheer))
	assert.True(t, cheered)
	assert.Equal(t, 200, bits)

	// The captured Power-up cost channel points, so give it a bits price too:
	// paying with bits must still not make it a cheer.
	paid := parse(powerUp)
	paid.Bits = 30
	_, cheered = cheerBits(paid)
	assert.False(t, cheered, "a Power-up is not a cheer")

	shared := parse(cheer)
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
