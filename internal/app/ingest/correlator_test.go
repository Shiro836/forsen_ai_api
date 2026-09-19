package ingest

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"app/db"

	"github.com/Its-donkey/kappopher/helix"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type rowMaker struct {
	made []uuid.UUID
}

func (m *rowMaker) create() (uuid.UUID, error) {
	id := uuid.New()
	m.made = append(m.made, id)
	return id, nil
}

func TestCorrelatorPairsEitherArrivalOrder(t *testing.T) {
	for _, order := range [][2]feed{{feedChat, feedEventSub}, {feedEventSub, feedChat}} {
		c, rows, now := newCorrelator(), &rowMaker{}, time.Now()
		key := newPairKey(1, 2, "reward", "test")

		_, paired, err := c.pair(key, order[0], now, rows.create)
		require.NoError(t, err)
		assert.False(t, paired)

		twin, paired, err := c.pair(key, order[1], now.Add(time.Second), rows.create)
		require.NoError(t, err)
		assert.True(t, paired)
		assert.Equal(t, rows.made[0], twin)
		assert.Len(t, rows.made, 1)
	}
}

func TestCorrelatorKeepsRepeatsApart(t *testing.T) {
	c, rows, now := newCorrelator(), &rowMaker{}, time.Now()
	key := newPairKey(1, 2, "reward", "test")

	for range 2 {
		_, paired, err := c.pair(key, feedChat, now, rows.create)
		require.NoError(t, err)
		assert.False(t, paired)
	}
	require.Len(t, rows.made, 2)

	for i := range 2 {
		twin, paired, err := c.pair(key, feedEventSub, now, rows.create)
		require.NoError(t, err)
		assert.True(t, paired)
		assert.Equal(t, rows.made[i], twin)
	}

	_, paired, err := c.pair(key, feedEventSub, now, rows.create)
	require.NoError(t, err)
	assert.False(t, paired)
	assert.Len(t, rows.made, 3)
}

func TestCorrelatorDifferentRedemptionsDoNotPair(t *testing.T) {
	c, rows, now := newCorrelator(), &rowMaker{}, time.Now()

	_, _, err := c.pair(newPairKey(1, 2, "reward", "test"), feedChat, now, rows.create)
	require.NoError(t, err)

	for _, other := range []pairKey{
		newPairKey(9, 2, "reward", "test"),
		newPairKey(1, 9, "reward", "test"),
		newPairKey(1, 2, "other", "test"),
		newPairKey(1, 2, "reward", "test 1"),
	} {
		_, paired, err := c.pair(other, feedEventSub, now, rows.create)
		require.NoError(t, err)
		assert.False(t, paired)
	}
}

func TestCorrelatorForgetsOldRows(t *testing.T) {
	c, rows, now := newCorrelator(), &rowMaker{}, time.Now()
	key := newPairKey(1, 2, "reward", "test")

	_, _, err := c.pair(key, feedChat, now, rows.create)
	require.NoError(t, err)

	_, paired, err := c.pair(key, feedEventSub, now.Add(correlatorTTL+time.Minute), rows.create)
	require.NoError(t, err)
	assert.False(t, paired)
}

func TestCorrelatorFailedCreateLeavesNothingBehind(t *testing.T) {
	c, rows, now := newCorrelator(), &rowMaker{}, time.Now()
	key := newPairKey(1, 2, "reward", "test")

	_, _, err := c.pair(key, feedChat, now, func() (uuid.UUID, error) { return uuid.Nil, errors.New("db down") })
	require.Error(t, err)

	_, paired, err := c.pair(key, feedEventSub, now, rows.create)
	require.NoError(t, err)
	assert.False(t, paired)
}

func TestPairKeyIgnoresInvisibleDifferences(t *testing.T) {
	assert.Equal(t, newPairKey(1, 2, "r", "hello world"), newPairKey(1, 2, "r", "  hello   world \U000E0000"))
	assert.Equal(t, newPairKey(1, 2, "r", "hello"), newPairKey(1, 2, "r", "hel​lo"))
	assert.NotEqual(t, newPairKey(1, 2, "r", "hello"), newPairKey(1, 2, "r", "Hello"))
}

func parsed(t *testing.T, eventType, event string) (string, *arrival) {
	t.Helper()
	broadcasterLogin, in, err := parseEvent(&helix.EventSubWebhookMessage{
		MessageID:        "msg-1",
		SubscriptionType: eventType,
		Event:            json.RawMessage(event),
	})
	require.NoError(t, err)
	return broadcasterLogin, in
}

// Payloads are real deliveries, captured 2026-09-19.
func TestParseRedemption(t *testing.T) {
	broadcasterLogin, points := parsed(t, helix.EventSubTypeChannelPointsRedemptionAdd,
		`{"broadcaster_user_id":"82054454","broadcaster_user_login":"shirogopher","broadcaster_user_name":"ShiroGopher","id":"e78aa3bd-3d8e-4c06-b9da-9f308e7d44cd","user_id":"82054454","user_login":"shirogopher","user_name":"ShiroGopher","user_input":"test","status":"unfulfilled","redeemed_at":"2026-09-19T15:34:08.228125857Z","reward":{"id":"a79c1221-3ab2-4367-a401-a8e333255abb","title":"BAJ TTS","prompt":"Voices: forsen.fun/voices","cost":10}}`)
	assert.Equal(t, "shirogopher", broadcasterLogin)
	assert.Equal(t, &arrival{
		msg: db.TwitchMessage{
			TwitchLogin:  "shirogopher",
			TwitchUserID: 82054454,
			Message:      "test",
			RewardID:     "a79c1221-3ab2-4367-a401-a8e333255abb",
			Event:        &db.EventMeta{Kind: db.EventKindPointsRedeem, RedemptionID: "e78aa3bd-3d8e-4c06-b9da-9f308e7d44cd"},
		},
		uniqueID: "eventsub:e78aa3bd-3d8e-4c06-b9da-9f308e7d44cd",
		pairAs:   "a79c1221-3ab2-4367-a401-a8e333255abb",
	}, points)

	_, powerUp := parsed(t, helix.EventSubTypeChannelCustomPowerUpRedemptionAdd,
		`{"broadcaster_user_id":"82054454","broadcaster_user_login":"shirogopher","broadcaster_user_name":"ShiroGopher","id":"82ce56df-c9e7-44c5-8da0-fe811c0106a1","user_id":"82054454","user_login":"shirogopher","user_name":"ShiroGopher","user_input":"test 1","status":"fulfilled","custom_power_up":{"id":"307cc91e-a5c0-4799-a10e-37bb9a2c8b61","title":"Test Reward","bits":10,"prompt":""},"redeemed_at":"2026-09-19T15:35:38.503620471Z"}`)
	assert.Equal(t, &arrival{
		msg: db.TwitchMessage{
			TwitchLogin:  "shirogopher",
			TwitchUserID: 82054454,
			Message:      "test 1",
			RewardID:     "307cc91e-a5c0-4799-a10e-37bb9a2c8b61",
			Event:        &db.EventMeta{Kind: db.EventKindCustomPowerUp, RedemptionID: "82ce56df-c9e7-44c5-8da0-fe811c0106a1", Bits: 10, USD: 0.1},
		},
		uniqueID: "eventsub:82ce56df-c9e7-44c5-8da0-fe811c0106a1",
		pairAs:   "307cc91e-a5c0-4799-a10e-37bb9a2c8b61",
	}, powerUp)

	_, _, err := parseEvent(&helix.EventSubWebhookMessage{SubscriptionType: helix.EventSubTypeChannelUpdate})
	assert.Error(t, err)
}

// Payloads follow the examples in Twitch's EventSub reference; none of them
// is a captured delivery.
func TestParseLaneEvents(t *testing.T) {
	const (
		viewer      = `"user_id":"1234","user_login":"cool_user","user_name":"Cool_User",`
		broadcaster = `"broadcaster_user_id":"1337","broadcaster_user_login":"cooler_user","broadcaster_user_name":"Cooler_User",`
	)
	message := func(text string, event db.EventMeta) db.TwitchMessage {
		return db.TwitchMessage{TwitchLogin: "cool_user", TwitchUserID: 1234, Message: text, Event: &event}
	}

	cases := []struct {
		eventType string
		event     string
		want      *arrival
	}{
		{
			helix.EventSubTypeChannelFollow,
			`{` + viewer + broadcaster + `"followed_at":"2020-07-15T18:16:11.17106713Z"}`,
			&arrival{msg: message("", db.EventMeta{Kind: db.EventKindFollow}), uniqueID: "eventsub:msg-1"},
		},
		{
			helix.EventSubTypeChannelSubscribe,
			`{` + viewer + broadcaster + `"tier":"1000","is_gift":false}`,
			&arrival{msg: message("", db.EventMeta{Kind: db.EventKindSub, Tier: 1}), uniqueID: "eventsub:msg-1"},
		},
		{
			helix.EventSubTypeChannelSubscribe,
			`{` + viewer + broadcaster + `"tier":"1000","is_gift":true}`,
			nil,
		},
		{
			helix.EventSubTypeChannelSubscriptionGift,
			`{` + viewer + broadcaster + `"total":2,"tier":"1000","cumulative_total":284,"is_anonymous":false}`,
			&arrival{msg: message("", db.EventMeta{Kind: db.EventKindGiftSubs, Tier: 1, GiftCount: 2}), uniqueID: "eventsub:msg-1"},
		},
		{
			helix.EventSubTypeChannelSubscriptionMessage,
			`{` + viewer + broadcaster + `"tier":"1000","message":{"text":"Love the stream! FevziGG","emotes":[{"begin":23,"end":30,"id":"302976485"}]},"cumulative_months":15,"streak_months":1,"duration_months":6}`,
			&arrival{
				msg:      message("Love the stream! FevziGG", db.EventMeta{Kind: db.EventKindResub, Tier: 1, Months: 15}),
				uniqueID: "eventsub:msg-1",
				pairAs:   pairAsResub,
			},
		},
		{
			helix.EventSubTypeChannelRaid,
			`{"from_broadcaster_user_id":"1234","from_broadcaster_user_login":"cool_user","from_broadcaster_user_name":"Cool_User","to_broadcaster_user_id":"1337","to_broadcaster_user_login":"cooler_user","to_broadcaster_user_name":"Cooler_User","viewers":9001}`,
			&arrival{msg: message("", db.EventMeta{Kind: db.EventKindRaid, Viewers: 9001}), uniqueID: "eventsub:msg-1"},
		},
		{
			helix.EventSubTypeChannelBitsUse,
			`{` + viewer + broadcaster + `"bits":2,"type":"cheer","power_up":null,"custom_power_up":null,"message":{"text":"cheer1 hi cheer1","fragments":[{"type":"cheermote","text":"cheer1","cheermote":{"prefix":"cheer","bits":1,"tier":1},"emote":null},{"type":"text","text":" hi ","cheermote":null,"emote":null},{"type":"cheermote","text":"cheer1","cheermote":{"prefix":"cheer","bits":1,"tier":1},"emote":null}]}}`,
			&arrival{
				msg:      message("cheer1 hi cheer1", db.EventMeta{Kind: db.EventKindCheer, Bits: 2, USD: 0.02}),
				uniqueID: "eventsub:msg-1",
				pairAs:   "cheer:2",
			},
		},
		{
			helix.EventSubTypeChannelBitsUse,
			`{` + viewer + broadcaster + `"bits":10,"type":"custom_power_up","power_up":null,"custom_power_up":{},"message":null}`,
			nil,
		},
	}

	for _, tc := range cases {
		broadcasterLogin, in := parsed(t, tc.eventType, tc.event)
		assert.Equal(t, tc.want, in, tc.eventType)
		if tc.want != nil {
			assert.Equal(t, "cooler_user", broadcasterLogin, tc.eventType)
		}
	}
}

func TestParseAnonymousGift(t *testing.T) {
	_, in := parsed(t, helix.EventSubTypeChannelSubscriptionGift,
		`{"user_id":null,"user_login":null,"user_name":null,"broadcaster_user_id":"1337","broadcaster_user_login":"cooler_user","broadcaster_user_name":"Cooler_User","total":5,"tier":"1000","cumulative_total":null,"is_anonymous":true}`)
	assert.Equal(t, anonymousLogin, in.msg.TwitchLogin)
	assert.Equal(t, 5, in.msg.Event.GiftCount)
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

func TestSubscriptionsFollowTheLanes(t *testing.T) {
	types := func(u *db.IngestUser) []string {
		var eventTypes []string
		for _, key := range desiredSubscriptions(u) {
			assert.Equal(t, "7", key.broadcasterID)
			eventTypes = append(eventTypes, key.eventType)
		}
		return eventTypes
	}

	untouched := &db.IngestUser{TwitchUserID: 7, HasRewardButton: true}
	assert.Equal(t, []string{helix.EventSubTypeChannelPointsRedemptionAdd}, types(untouched))

	everything := &db.IngestUser{TwitchUserID: 7, HasRewardButton: true, TokenScopes: []string{scopeBitsRead}}
	for _, lane := range []db.MsgClass{db.MsgClassSub, db.MsgClassRaid, db.MsgClassFollow} {
		everything.Settings.SetLaneEnabled(lane, true)
	}
	assert.ElementsMatch(t, []string{
		helix.EventSubTypeChannelPointsRedemptionAdd,
		helix.EventSubTypeChannelCustomPowerUpRedemptionAdd,
		helix.EventSubTypeChannelBitsUse,
		helix.EventSubTypeChannelSubscribe,
		helix.EventSubTypeChannelSubscriptionMessage,
		helix.EventSubTypeChannelSubscriptionGift,
		helix.EventSubTypeChannelRaid,
		helix.EventSubTypeChannelFollow,
	}, types(everything))

	assert.Equal(t, map[string]string{"to_broadcaster_user_id": "7"}, subKey{helix.EventSubTypeChannelRaid, "7"}.condition())
	assert.Equal(t, map[string]string{"broadcaster_user_id": "7", "moderator_user_id": "7"}, subKey{helix.EventSubTypeChannelFollow, "7"}.condition())
}
