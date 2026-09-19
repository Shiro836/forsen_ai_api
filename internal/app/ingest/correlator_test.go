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
		key := newRedeemKey(1, 2, "reward", "test")

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
	key := newRedeemKey(1, 2, "reward", "test")

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

	_, _, err := c.pair(newRedeemKey(1, 2, "reward", "test"), feedChat, now, rows.create)
	require.NoError(t, err)

	for _, other := range []redeemKey{
		newRedeemKey(9, 2, "reward", "test"),
		newRedeemKey(1, 9, "reward", "test"),
		newRedeemKey(1, 2, "other", "test"),
		newRedeemKey(1, 2, "reward", "test 1"),
	} {
		_, paired, err := c.pair(other, feedEventSub, now, rows.create)
		require.NoError(t, err)
		assert.False(t, paired)
	}
}

func TestCorrelatorForgetsOldRows(t *testing.T) {
	c, rows, now := newCorrelator(), &rowMaker{}, time.Now()
	key := newRedeemKey(1, 2, "reward", "test")

	_, _, err := c.pair(key, feedChat, now, rows.create)
	require.NoError(t, err)

	_, paired, err := c.pair(key, feedEventSub, now.Add(correlatorTTL+time.Minute), rows.create)
	require.NoError(t, err)
	assert.False(t, paired)
}

func TestCorrelatorFailedCreateLeavesNothingBehind(t *testing.T) {
	c, rows, now := newCorrelator(), &rowMaker{}, time.Now()
	key := newRedeemKey(1, 2, "reward", "test")

	_, _, err := c.pair(key, feedChat, now, func() (uuid.UUID, error) { return uuid.Nil, errors.New("db down") })
	require.Error(t, err)

	_, paired, err := c.pair(key, feedEventSub, now, rows.create)
	require.NoError(t, err)
	assert.False(t, paired)
}

func TestRedeemKeyIgnoresInvisibleDifferences(t *testing.T) {
	assert.Equal(t, newRedeemKey(1, 2, "r", "hello world"), newRedeemKey(1, 2, "r", "  hello   world \U000E0000"))
	assert.Equal(t, newRedeemKey(1, 2, "r", "hello"), newRedeemKey(1, 2, "r", "hel​lo"))
	assert.NotEqual(t, newRedeemKey(1, 2, "r", "hello"), newRedeemKey(1, 2, "r", "Hello"))
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
		viewerID:         "82054454",
		viewerLogin:      "shirogopher",
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
		viewerID:         "82054454",
		viewerLogin:      "shirogopher",
		rewardID:         "307cc91e-a5c0-4799-a10e-37bb9a2c8b61",
		text:             "test 1",
		event:            db.EventMeta{Kind: db.EventKindCustomPowerUp, RedemptionID: "82ce56df-c9e7-44c5-8da0-fe811c0106a1", Bits: 10, USD: 0.1},
	}, powerUp)

	_, err = parseRedemption(&helix.EventSubWebhookMessage{SubscriptionType: helix.EventSubTypeChannelBitsUse})
	assert.Error(t, err)
}
