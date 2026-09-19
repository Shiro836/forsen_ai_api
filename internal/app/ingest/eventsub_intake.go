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

// arrival is one feed's delivery of an event.
type arrival struct {
	msg      db.TwitchMessage
	uniqueID string
	// pairAs is set when the other feed delivers the same event.
	pairAs string
}

const (
	pairAsResub    = "resub"
	anonymousLogin = "anonymous"
)

func pairAsCheer(bits int) string {
	return "cheer:" + strconv.Itoa(bits)
}

// Chat says "Prime" where the event feed says "1000".
func subTier(plan string) int {
	switch plan {
	case "1000", "Prime":
		return 1
	case "2000":
		return 2
	case "3000":
		return 3
	}
	return 0
}

func viewerMessage(user helix.EventSubUser, text string, event db.EventMeta) db.TwitchMessage {
	viewerID, _ := strconv.Atoi(user.UserID)
	return db.TwitchMessage{
		TwitchLogin:  user.UserLogin,
		TwitchUserID: viewerID,
		Message:      text,
		Event:        &event,
	}
}

// parseEvent returns no arrival for a notification that is not a message of
// its own: a gift recipient's sub, bits spent on anything but a cheer.
func parseEvent(msg *helix.EventSubWebhookMessage) (broadcasterLogin string, in *arrival, err error) {
	uniqueID := "eventsub:" + msg.MessageID

	switch msg.SubscriptionType {
	case helix.EventSubTypeChannelPointsRedemptionAdd:
		ev, err := helix.ParseEventSubEvent[helix.ChannelPointsRedemptionAddEvent](msg)
		if err != nil {
			return "", nil, err
		}
		in = &arrival{
			msg:      viewerMessage(ev.EventSubUser, ev.UserInput, db.EventMeta{Kind: db.EventKindPointsRedeem, RedemptionID: ev.ID}),
			uniqueID: "eventsub:" + ev.ID,
			pairAs:   ev.Reward.ID,
		}
		in.msg.RewardID = ev.Reward.ID
		return ev.BroadcasterUserLogin, in, nil

	case helix.EventSubTypeChannelCustomPowerUpRedemptionAdd:
		ev, err := helix.ParseEventSubEvent[helix.ChannelCustomPowerUpRedemptionAddEvent](msg)
		if err != nil {
			return "", nil, err
		}
		in = &arrival{
			msg: viewerMessage(ev.EventSubUser, ev.UserInput, db.EventMeta{
				Kind:         db.EventKindCustomPowerUp,
				RedemptionID: ev.ID,
				Bits:         ev.CustomPowerUp.Bits,
				USD:          float64(ev.CustomPowerUp.Bits) / db.BitsPerUSD,
			}),
			uniqueID: "eventsub:" + ev.ID,
			pairAs:   ev.CustomPowerUp.ID,
		}
		in.msg.RewardID = ev.CustomPowerUp.ID
		return ev.BroadcasterUserLogin, in, nil

	case helix.EventSubTypeChannelBitsUse:
		ev, err := helix.ParseEventSubEvent[helix.ChannelBitsUseEvent](msg)
		if err != nil {
			return "", nil, err
		}
		if ev.Type != "cheer" || ev.Message == nil {
			return "", nil, nil
		}
		return ev.BroadcasterUserLogin, &arrival{
			msg: viewerMessage(ev.EventSubUser, ev.Message.Text, db.EventMeta{
				Kind: db.EventKindCheer,
				Bits: ev.BitsUsed,
				USD:  float64(ev.BitsUsed) / db.BitsPerUSD,
			}),
			uniqueID: uniqueID,
			pairAs:   pairAsCheer(ev.BitsUsed),
		}, nil

	case helix.EventSubTypeChannelSubscribe:
		ev, err := helix.ParseEventSubEvent[helix.ChannelSubscribeEvent](msg)
		if err != nil {
			return "", nil, err
		}
		// The gift itself is announced once, by channel.subscription.gift.
		if ev.IsGift {
			return "", nil, nil
		}
		return ev.BroadcasterUserLogin, &arrival{
			msg:      viewerMessage(ev.EventSubUser, "", db.EventMeta{Kind: db.EventKindSub, Tier: subTier(ev.Tier)}),
			uniqueID: uniqueID,
		}, nil

	case helix.EventSubTypeChannelSubscriptionMessage:
		ev, err := helix.ParseEventSubEvent[helix.ChannelSubscriptionMessageEvent](msg)
		if err != nil {
			return "", nil, err
		}
		return ev.BroadcasterUserLogin, &arrival{
			msg: viewerMessage(ev.EventSubUser, ev.Message.Text, db.EventMeta{
				Kind:   db.EventKindResub,
				Tier:   subTier(ev.Tier),
				Months: ev.CumulativeMonths,
			}),
			uniqueID: uniqueID,
			pairAs:   pairAsResub,
		}, nil

	case helix.EventSubTypeChannelSubscriptionGift:
		ev, err := helix.ParseEventSubEvent[helix.ChannelSubscriptionGiftEvent](msg)
		if err != nil {
			return "", nil, err
		}
		in = &arrival{
			msg:      viewerMessage(ev.EventSubUser, "", db.EventMeta{Kind: db.EventKindGiftSubs, Tier: subTier(ev.Tier), GiftCount: ev.Total}),
			uniqueID: uniqueID,
		}
		if ev.IsAnonymous {
			in.msg.TwitchLogin = anonymousLogin
		}
		return ev.BroadcasterUserLogin, in, nil

	case helix.EventSubTypeChannelFollow:
		ev, err := helix.ParseEventSubEvent[helix.ChannelFollowEvent](msg)
		if err != nil {
			return "", nil, err
		}
		return ev.BroadcasterUserLogin, &arrival{
			msg:      viewerMessage(ev.EventSubUser, "", db.EventMeta{Kind: db.EventKindFollow}),
			uniqueID: uniqueID,
		}, nil

	case helix.EventSubTypeChannelRaid:
		ev, err := helix.ParseEventSubEvent[helix.ChannelRaidEvent](msg)
		if err != nil {
			return "", nil, err
		}
		raider := helix.EventSubUser{UserID: ev.FromBroadcasterUserID, UserLogin: ev.FromBroadcasterUserLogin}
		return ev.ToBroadcasterUserLogin, &arrival{
			msg:      viewerMessage(raider, "", db.EventMeta{Kind: db.EventKindRaid, Viewers: ev.Viewers}),
			uniqueID: uniqueID,
		}, nil
	}

	return "", nil, fmt.Errorf("no intake for event type %q", msg.SubscriptionType)
}

func (s *Service) handleEventSub(ctx context.Context, msg *helix.EventSubWebhookMessage) {
	broadcasterLogin, in, err := parseEvent(msg)
	if err != nil {
		s.logger.Error("failed to parse eventsub notification", "err", err, "type", msg.SubscriptionType, "event", string(msg.Event))
		return
	}
	if in == nil {
		return
	}

	s.activeUsersLock.RLock()
	userCfg, ok := s.activeUsers[strings.ToLower(broadcasterLogin)]
	s.activeUsersLock.RUnlock()
	if !ok {
		return
	}

	// Chat never delivers a redemption without text, and hands "^^" text to
	// clanker instead of the queue; the same redemption must not enter from here.
	if in.msg.RewardID != "" && (len(in.msg.Message) == 0 || strings.HasPrefix(in.msg.Message, "^^")) {
		return
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	s.push(ctx, broadcasterLogin, userCfg, *in, feedEventSub)
}
