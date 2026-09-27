package ingest

import (
	"encoding/json"
	"testing"

	"app/db"

	"github.com/Its-donkey/kappopher/helix"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func event[T any](t *testing.T, payload string) *T {
	t.Helper()
	var ev T
	require.NoError(t, json.Unmarshal([]byte(payload), &ev))
	return &ev
}

// Each pair is one real message on shirogopher, 2026-09-20, as chat delivered
// it and as the event feed delivered it.
const (
	chatLineIRC    = `@badge-info=subscriber/36;badges=broadcaster/1,subscriber/0,hype-train/1;client-nonce=5499689dda92463d944b570b6dc51255;color=#FF69B4;display-name=ShiroGopher;emotes=;first-msg=0;flags=;id=8a6c1d27-0857-4f64-b016-3d7a23586b6c;mod=0;returning-chatter=0;room-id=82054454;subscriber=1;tmi-sent-ts=1789911966174;turbo=0;user-id=82054454;user-type= :shirogopher!shirogopher@shirogopher.tmi.twitch.tv PRIVMSG #shirogopher :test 5`
	chatLineEvents = `{"broadcaster_user_id":"82054454","broadcaster_user_login":"shirogopher","broadcaster_user_name":"ShiroGopher","source_broadcaster_user_id":null,"source_broadcaster_user_login":null,"source_broadcaster_user_name":null,"chatter_user_id":"82054454","chatter_user_login":"shirogopher","chatter_user_name":"ShiroGopher","message_id":"8a6c1d27-0857-4f64-b016-3d7a23586b6c","source_message_id":null,"is_source_only":null,"message":{"text":"test 5","fragments":[{"type":"text","text":"test 5","cheermote":null,"emote":null,"mention":null,"gif":null}]},"color":"#FF69B4","badges":[{"set_id":"broadcaster","id":"1","info":""},{"set_id":"subscriber","id":"0","info":"36"},{"set_id":"hype-train","id":"1","info":""}],"source_badges":null,"message_type":"text","cheer":null,"reply":null,"channel_points_custom_reward_id":null,"channel_points_animation_id":null}`

	redeemIRC    = `@badge-info=subscriber/36;badges=broadcaster/1,subscriber/0,hype-train/1;color=#FF69B4;custom-reward-id=a79c1221-3ab2-4367-a401-a8e333255abb;display-name=ShiroGopher;emotes=;first-msg=0;flags=;id=d1f61f39-37cb-4839-95de-e124d4230a1f;mod=0;returning-chatter=0;room-id=82054454;subscriber=1;tmi-sent-ts=1789911986277;turbo=0;user-id=82054454;user-type= :shirogopher!shirogopher@shirogopher.tmi.twitch.tv PRIVMSG #shirogopher :test 6`
	redeemEvents = `{"broadcaster_user_id":"82054454","broadcaster_user_login":"shirogopher","broadcaster_user_name":"ShiroGopher","source_broadcaster_user_id":null,"source_broadcaster_user_login":null,"source_broadcaster_user_name":null,"chatter_user_id":"82054454","chatter_user_login":"shirogopher","chatter_user_name":"ShiroGopher","message_id":"d1f61f39-37cb-4839-95de-e124d4230a1f","source_message_id":null,"is_source_only":null,"message":{"text":"test 6","fragments":[{"type":"text","text":"test 6","cheermote":null,"emote":null,"mention":null,"gif":null}]},"color":"#FF69B4","badges":[{"set_id":"broadcaster","id":"1","info":""},{"set_id":"subscriber","id":"0","info":"36"},{"set_id":"hype-train","id":"1","info":""}],"source_badges":null,"message_type":"text","cheer":null,"reply":null,"channel_points_custom_reward_id":"a79c1221-3ab2-4367-a401-a8e333255abb","channel_points_animation_id":null}`
)

func TestBothFeedsDeliverTheSameChatLine(t *testing.T) {
	for _, pair := range [][2]string{{chatLineIRC, chatLineEvents}, {redeemIRC, redeemEvents}} {
		fromChat := ircChatLine(chatMessage(t, pair[0]))
		fromEvents := chatMessageLine(event[helix.ChannelChatMessageEvent](t, pair[1]))

		assert.NotEmpty(t, fromChat.id)
		assert.Equal(t, fromChat, fromEvents)
	}

	redeem := ircChatLine(chatMessage(t, redeemIRC))
	assert.Equal(t, "a79c1221-3ab2-4367-a401-a8e333255abb", redeem.rewardID)
}

// A cheer on the event feed was never captured; this payload follows the
// reference, and the chat line beside it is real with the viewer replaced.
func TestBothFeedsDeliverTheSameCheer(t *testing.T) {
	fromChat := ircChatLine(chatMessage(t, cheerMessage))

	fromEvents := chatMessageLine(event[helix.ChannelChatMessageEvent](t, `{"broadcaster_user_id":"48878319","broadcaster_user_login":"streamer","chatter_user_id":"202021370","chatter_user_login":"some_viewer","message_id":"0b91cd7d-01f2-4f3e-b23a-6d38e55df643","message":{"text":"Cheer200 lo de verte en stream","fragments":[]},"message_type":"text","cheer":{"bits":200},"channel_points_custom_reward_id":null}`))

	assert.Equal(t, 200, fromChat.cheered)
	assert.Equal(t, fromChat, fromEvents)

	elsewhere := chatMessageLine(event[helix.ChannelChatMessageEvent](t, `{"broadcaster_user_id":"1","broadcaster_user_login":"streamer","source_broadcaster_user_id":"2","chatter_user_id":"3","chatter_user_login":"some_viewer","message_id":"m","message":{"text":"Cheer200 hi"},"cheer":{"bits":200}}`))
	assert.Zero(t, elsewhere.cheered, "a cheer in another channel's shared chat is not ours")
}

// The announcement is a real delivery (shirogopher, 2026-09-20). No other kind
// of notice was ever captured on the event feed: those payloads follow the
// reference, each built to say what the real chat notice beside it says.
func TestBothFeedsDeliverTheSameNotice(t *testing.T) {
	_, ok := notificationArrival(event[helix.ChannelChatNotificationEvent](t, `{"broadcaster_user_id":"82054454","broadcaster_user_login":"shirogopher","broadcaster_user_name":"ShiroGopher","chatter_user_id":"82054454","chatter_user_login":"shirogopher","chatter_user_name":"ShiroGopher","chatter_is_anonymous":false,"color":"#FF69B4","system_message":"","message_id":"0dc77f6c-d265-47aa-8a72-05b659c4dbca","message":{"text":"test 7","fragments":[{"type":"text","text":"test 7","cheermote":null,"emote":null,"mention":null}]},"notice_type":"announcement","sub":null,"resub":null,"sub_gift":null,"community_sub_gift":null,"gift_paid_upgrade":null,"prime_paid_upgrade":null,"pay_it_forward":null,"raid":null,"unraid":null,"announcement":{"color":"PRIMARY"},"watch_streak":null}`))
	assert.False(t, ok, "an announcement is not an event we play")

	const streamer = `"broadcaster_user_id":"1","broadcaster_user_login":"streamer",`
	viewer := func(id, login string) string {
		return `"chatter_user_id":"` + id + `","chatter_user_login":"` + login + `","chatter_is_anonymous":false,`
	}

	cases := []struct {
		name   string
		chat   string
		events string
	}{
		{"new sub", subNotice,
			`{` + streamer + viewer("424549349", "some_viewer") + `"message_id":"5c2588c3-5ca7-4cad-a176-1912c99933b8","message":{"text":""},"notice_type":"sub","sub":{"sub_tier":"1000","is_prime":true,"duration_months":1}}`},
		{"prime upgrade", primeUpgrade,
			`{` + streamer + viewer("92060342", "some_viewer") + `"message_id":"dcff9944-cd36-495f-a564-8d2658a8a5d4","message":{"text":""},"notice_type":"prime_paid_upgrade","prime_paid_upgrade":{"sub_tier":"1000"}}`},
		{"gift upgrade", giftUpgrade,
			`{` + streamer + viewer("722315869", "some_viewer") + `"message_id":"0682ab80-8f03-4fe5-a02c-abd36a491462","message":{"text":""},"notice_type":"gift_paid_upgrade","gift_paid_upgrade":{"gifter_is_anonymous":false,"gifter_user_login":"someone"}}`},
		{"resub with words", resubNotice,
			`{` + streamer + viewer("137578120", "some_viewer") + `"message_id":"68b7c055-2b27-42c7-84b5-6c2cd3e90dd1","message":{"text":"hope you're doing fine"},"notice_type":"resub","resub":{"cumulative_months":60,"duration_months":24,"sub_tier":"1000","is_prime":false,"is_gift":false}}`},
		{"community gift", mysteryGift,
			`{` + streamer + viewer("42467504", "some_gifter") + `"message_id":"6d412482-f5d5-43ed-9a64-4cc2e2a82132","message":{"text":""},"notice_type":"community_sub_gift","community_sub_gift":{"id":"17694935589118822615","total":5,"sub_tier":"1000"}}`},
		{"gift to one viewer", standaloneGift,
			`{` + streamer + viewer("162957501", "some_gifter") + `"message_id":"f70d0ea6-16c6-4c17-addb-a8da1af874ed","message":{"text":""},"notice_type":"sub_gift","sub_gift":{"duration_months":1,"recipient_user_id":"1496027923","recipient_user_login":"some_viewer","sub_tier":"1000","community_gift_id":null}}`},
		{"anonymous gift", anonymousGift,
			`{` + streamer + `"chatter_user_id":"274598607","chatter_user_login":"ananonymousgifter","chatter_is_anonymous":true,"message_id":"fdfa1527-3499-4881-b8e2-efd7f463b44d","message":{"text":""},"notice_type":"sub_gift","sub_gift":{"duration_months":1,"recipient_user_id":"123716893","recipient_user_login":"some_viewer","sub_tier":"1000","community_gift_id":null}}`},
		{"raid", raidNotice,
			`{` + streamer + viewer("150480078", "some_raider") + `"message_id":"49c7fe4b-1cd7-4afe-a727-c492331657d6","message":{"text":""},"notice_type":"raid","raid":{"user_id":"150480078","user_login":"some_raider","viewer_count":91}}`},
		{"watch streak", streakNotice,
			`{` + streamer + viewer("125629679", "some_viewer") + `"message_id":"9807c606-e5b3-4dd0-a066-4ea5fdbadff3","message":{"text":"hello"},"notice_type":"watch_streak","watch_streak":{"streak_count":25,"channel_points_awarded":450}}`},
	}
	for _, tc := range cases {
		fromChat, ok := noticeArrival(notice(t, tc.chat))
		require.True(t, ok, tc.name)
		fromEvents, ok := notificationArrival(event[helix.ChannelChatNotificationEvent](t, tc.events))
		require.True(t, ok, tc.name)

		assert.Equal(t, fromChat, fromEvents, tc.name)
	}

	for name, events := range map[string]string{
		"recipient of a community gift": `{` + streamer + viewer("42467504", "some_gifter") + `"message_id":"m","message":{"text":""},"notice_type":"sub_gift","sub_gift":{"sub_tier":"1000","community_gift_id":"17694935589118822615"}}`,
		"gift paid forward":             `{` + streamer + viewer("95315270", "some_viewer") + `"message_id":"m","message":{"text":""},"notice_type":"pay_it_forward","pay_it_forward":{}}`,
		"sub in another channel":        `{` + streamer + viewer("1", "some_viewer") + `"message_id":"m","message":{"text":""},"notice_type":"shared_chat_sub","shared_chat_sub":{"sub_tier":"1000"}}`,
	} {
		_, ok := notificationArrival(event[helix.ChannelChatNotificationEvent](t, events))
		assert.False(t, ok, name)
	}
}

// Payloads are real deliveries, captured 2026-09-19.
func TestParseRedemption(t *testing.T) {
	points, err := parseRedemption(&helix.EventSubWebhookMessage{
		SubscriptionType: helix.EventSubTypeChannelPointsRedemptionAdd,
		Event:            json.RawMessage(`{"broadcaster_user_id":"82054454","broadcaster_user_login":"shirogopher","broadcaster_user_name":"ShiroGopher","id":"e78aa3bd-3d8e-4c06-b9da-9f308e7d44cd","user_id":"82054454","user_login":"shirogopher","user_name":"ShiroGopher","user_input":"test","status":"unfulfilled","redeemed_at":"2026-09-19T15:34:08.228125857Z","reward":{"id":"a79c1221-3ab2-4367-a401-a8e333255abb","title":"BAJ TTS","prompt":"Voices: forsen.fun/voices","cost":10}}`),
	})
	require.NoError(t, err)
	assert.Equal(t, &redemption{
		broadcasterLogin: "shirogopher",
		viewerID:         82054454,
		rewardID:         "a79c1221-3ab2-4367-a401-a8e333255abb",
		text:             "test",
		event:            db.EventMeta{Kind: db.EventKindPointsRedeem, RedemptionID: "e78aa3bd-3d8e-4c06-b9da-9f308e7d44cd"},
	}, points)

	powerUp, err := parseRedemption(&helix.EventSubWebhookMessage{
		SubscriptionType: helix.EventSubTypeChannelCustomPowerUpRedemptionAdd,
		Event:            json.RawMessage(`{"broadcaster_user_id":"82054454","broadcaster_user_login":"shirogopher","broadcaster_user_name":"ShiroGopher","id":"82ce56df-c9e7-44c5-8da0-fe811c0106a1","user_id":"82054454","user_login":"shirogopher","user_name":"ShiroGopher","user_input":"test 1","status":"fulfilled","custom_power_up":{"id":"307cc91e-a5c0-4799-a10e-37bb9a2c8b61","title":"Test Reward","bits":10,"prompt":""},"redeemed_at":"2026-09-19T15:35:38.503620471Z"}`),
	})
	require.NoError(t, err)
	assert.Equal(t, &redemption{
		broadcasterLogin: "shirogopher",
		viewerID:         82054454,
		rewardID:         "307cc91e-a5c0-4799-a10e-37bb9a2c8b61",
		text:             "test 1",
		event:            db.EventMeta{Kind: db.EventKindCustomPowerUp, RedemptionID: "82ce56df-c9e7-44c5-8da0-fe811c0106a1", Bits: 10, USD: 0.1},
	}, powerUp)
}

func TestRedeemKeyIgnoresInvisibleDifferences(t *testing.T) {
	assert.Equal(t, redeemKey(2, "r", "hello world"), redeemKey(2, "r", "  hello   world \U000E0000"))
	assert.Equal(t, redeemKey(2, "r", "hello"), redeemKey(2, "r", "hel​lo"))

	for name, other := range map[string]string{
		"case":    redeemKey(2, "r", "Hello"),
		"viewer":  redeemKey(9, "r", "hello"),
		"reward":  redeemKey(2, "other", "hello"),
		"spacing": redeemKey(2, "r", "hel lo"),
	} {
		assert.NotEqual(t, redeemKey(2, "r", "hello"), other, name)
	}
}

func TestCheermoteWord(t *testing.T) {
	word := cheermoteWord([]string{"Cheer", "uni", "4Head"})

	for _, spends := range []string{"Cheer100", "cheer1", "UNI50", "4head10"} {
		assert.True(t, word.MatchString(spends), spends)
	}
	for _, said := range []string{"cheer", "gta6", "Cheer100!", "100", "cheers2"} {
		assert.False(t, word.MatchString(said), said)
	}
}

func TestSubscriptions(t *testing.T) {
	types := func(u *db.IngestUser) []string {
		var eventTypes []string
		for _, key := range desiredSubscriptions(u) {
			assert.Equal(t, "7", key.broadcasterID)
			eventTypes = append(eventTypes, key.eventType)
		}
		return eventTypes
	}

	oldLogin := &db.IngestUser{TwitchUserID: 7, HasRewardButton: true}
	assert.Equal(t, []string{helix.EventSubTypeChannelPointsRedemptionAdd}, types(oldLogin),
		"without the chat scopes there is no second copy of chat to ask for")

	newLogin := &db.IngestUser{TwitchUserID: 7, HasRewardButton: true, TokenScopes: append([]string{scopeBitsRead}, chatScopes...)}
	assert.ElementsMatch(t, []string{
		helix.EventSubTypeChannelPointsRedemptionAdd,
		helix.EventSubTypeChannelCustomPowerUpRedemptionAdd,
		helix.EventSubTypeChannelChatMessage,
	}, types(newLogin))

	for _, lane := range []db.MsgClass{db.MsgClassSub, db.MsgClassFollow} {
		newLogin.Settings.SetLaneEnabled(lane, true)
	}
	assert.ElementsMatch(t, []string{
		helix.EventSubTypeChannelPointsRedemptionAdd,
		helix.EventSubTypeChannelCustomPowerUpRedemptionAdd,
		helix.EventSubTypeChannelChatMessage,
		helix.EventSubTypeChannelChatNotification,
		helix.EventSubTypeChannelFollow,
	}, types(newLogin))

	partial := &db.IngestUser{TwitchUserID: 7, HasRewardButton: true, TokenScopes: []string{"user:read:chat", "user:bot"}}
	assert.NotContains(t, types(partial), helix.EventSubTypeChannelChatMessage, "all three chat scopes or none")

	assert.Equal(t, map[string]string{"broadcaster_user_id": "7", "user_id": "7"}, subKey{helix.EventSubTypeChannelChatMessage, "7"}.condition())
	assert.Equal(t, map[string]string{"broadcaster_user_id": "7", "moderator_user_id": "7"}, subKey{helix.EventSubTypeChannelFollow, "7"}.condition())
}
