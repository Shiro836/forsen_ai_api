package ingest

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"app/db"

	"github.com/Its-donkey/kappopher/helix"
)

// EventSubHandler is nil when EventSub is not configured.
func (s *Service) EventSubHandler() http.Handler {
	if s.eventSub == nil {
		return nil
	}
	return s.eventSub.handler()
}

type redemption struct {
	broadcasterLogin string
	viewerID         string
	viewerLogin      string
	rewardID         string
	text             string
	event            db.EventMeta
}

func parseRedemption(msg *helix.EventSubWebhookMessage) (*redemption, error) {
	switch msg.SubscriptionType {
	case helix.EventSubTypeChannelPointsRedemptionAdd:
		ev, err := helix.ParseEventSubEvent[helix.ChannelPointsRedemptionAddEvent](msg)
		if err != nil {
			return nil, err
		}
		return &redemption{
			broadcasterLogin: ev.BroadcasterUserLogin,
			viewerID:         ev.UserID,
			viewerLogin:      ev.UserLogin,
			rewardID:         ev.Reward.ID,
			text:             ev.UserInput,
			event:            db.EventMeta{Kind: db.EventKindPointsRedeem, RedemptionID: ev.ID},
		}, nil

	case helix.EventSubTypeChannelCustomPowerUpRedemptionAdd:
		ev, err := helix.ParseEventSubEvent[helix.ChannelCustomPowerUpRedemptionAddEvent](msg)
		if err != nil {
			return nil, err
		}
		return &redemption{
			broadcasterLogin: ev.BroadcasterUserLogin,
			viewerID:         ev.UserID,
			viewerLogin:      ev.UserLogin,
			rewardID:         ev.CustomPowerUp.ID,
			text:             ev.UserInput,
			event: db.EventMeta{
				Kind:         db.EventKindCustomPowerUp,
				RedemptionID: ev.ID,
				Bits:         ev.CustomPowerUp.Bits,
				USD:          float64(ev.CustomPowerUp.Bits) / db.BitsPerUSD,
			},
		}, nil
	}

	return nil, fmt.Errorf("no intake for event type %q", msg.SubscriptionType)
}

func (s *Service) handleEventSub(ctx context.Context, msg *helix.EventSubWebhookMessage) {
	redeem, err := parseRedemption(msg)
	if err != nil {
		s.logger.Error("failed to parse eventsub notification", "err", err, "type", msg.SubscriptionType, "event", string(msg.Event))
		return
	}

	s.activeUsersLock.RLock()
	userCfg, ok := s.activeUsers[strings.ToLower(redeem.broadcasterLogin)]
	s.activeUsersLock.RUnlock()
	if !ok {
		return
	}

	// Chat never delivers a redemption without text, and hands "^^" text to
	// clanker instead of the queue; the same redemption must not enter from here.
	if len(redeem.text) == 0 || strings.HasPrefix(redeem.text, "^^") {
		return
	}

	viewerID, _ := strconv.Atoi(redeem.viewerID)

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	s.pushRedeem(ctx, redeem.broadcasterLogin, userCfg, db.TwitchMessage{
		TwitchLogin:  redeem.viewerLogin,
		TwitchUserID: viewerID,
		Message:      redeem.text,
		RewardID:     redeem.rewardID,
		Event:        &redeem.event,
	}, "eventsub:"+redeem.event.RedemptionID, feedEventSub)
}
