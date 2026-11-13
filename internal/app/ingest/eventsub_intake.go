package ingest

import (
	"context"
	"errors"
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

// arrival is a message on its way into the queue, under Twitch's id for it.
type arrival struct {
	msg      db.TwitchMessage
	uniqueID string
}

const (
	anonymousLogin = "anonymous"
	// The account chat puts in place of whoever gifted anonymously.
	anonymousGifterLogin = "ananonymousgifter"
)

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

// chatMessageLine is the event feed's copy of a chat message.
func chatMessageLine(ev *helix.ChannelChatMessageEvent) chatLine {
	viewerID, _ := strconv.Atoi(ev.ChatterUserID)

	line := chatLine{
		channel:  ev.BroadcasterUserLogin,
		id:       ev.MessageID,
		viewerID: viewerID,
		viewer:   ev.ChatterUserLogin,
		text:     ev.Message.Text,
		rewardID: ev.ChannelPointsCustomRewardID,
	}

	elsewhere := ev.SourceBroadcasterUserID != nil && *ev.SourceBroadcasterUserID != ev.BroadcasterUserID
	if ev.Cheer != nil && !elsewhere {
		line.cheered = ev.Cheer.Bits
	}
	return line
}

// notificationArrival is the event feed's copy of a chat notice; it has to
// answer exactly as noticeArrival does for the same notice.
func notificationArrival(ev *helix.ChannelChatNotificationEvent) (in arrival, ok bool) {
	viewerID, _ := strconv.Atoi(ev.ChatterUserID)
	in = arrival{
		msg:      db.TwitchMessage{TwitchLogin: ev.ChatterUserLogin, TwitchUserID: viewerID, Message: ev.Message.Text},
		uniqueID: ev.MessageID,
	}
	if ev.ChatterIsAnonymous {
		in.msg.TwitchLogin, in.msg.TwitchUserID = anonymousLogin, 0
	}

	// A notice of another channel in a shared chat has a type of its own
	// (shared_chat_sub and so on) and ends up in the default case.
	switch {
	case ev.NoticeType == "sub" && ev.Sub != nil:
		in.msg.Event = &db.EventMeta{Kind: db.EventKindSub, Tier: subTier(ev.Sub.SubTier)}
	case ev.NoticeType == "prime_paid_upgrade" && ev.PrimePaidUpgrade != nil:
		in.msg.Event = &db.EventMeta{Kind: db.EventKindSub, Tier: subTier(ev.PrimePaidUpgrade.SubTier)}
	case ev.NoticeType == "gift_paid_upgrade":
		in.msg.Event = &db.EventMeta{Kind: db.EventKindSub}
	case ev.NoticeType == "resub" && ev.Resub != nil:
		in.msg.Event = &db.EventMeta{Kind: db.EventKindResub, Tier: subTier(ev.Resub.SubTier), Months: ev.Resub.CumulativeMonths}
	case ev.NoticeType == "community_sub_gift" && ev.CommunitySubGift != nil:
		in.msg.Event = &db.EventMeta{Kind: db.EventKindGiftSubs, Tier: subTier(ev.CommunitySubGift.SubTier), GiftCount: ev.CommunitySubGift.Total}
	case ev.NoticeType == "sub_gift" && ev.SubGift != nil && (ev.SubGift.CommunityGiftID == nil || *ev.SubGift.CommunityGiftID == ""):
		in.msg.Event = &db.EventMeta{Kind: db.EventKindGiftSubs, Tier: subTier(ev.SubGift.SubTier), GiftCount: 1}
	case ev.NoticeType == "raid" && ev.Raid != nil:
		in.msg.Event = &db.EventMeta{Kind: db.EventKindRaid, Viewers: ev.Raid.ViewerCount}
	case ev.NoticeType == "watch_streak" && ev.WatchStreak != nil:
		in.msg.Event = &db.EventMeta{Kind: db.EventKindStreak, Streak: ev.WatchStreak.StreakCount}
	default:
		return arrival{}, false
	}

	return in, true
}

// redemption is Twitch's record of a redeem. It is not a message: it only adds
// what chat never says to the message the redeem was made with.
type redemption struct {
	broadcasterLogin string
	viewerID         int
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
		viewerID, _ := strconv.Atoi(ev.UserID)
		return &redemption{
			broadcasterLogin: ev.BroadcasterUserLogin,
			viewerID:         viewerID,
			rewardID:         ev.Reward.ID,
			text:             ev.UserInput,
			event:            db.EventMeta{Kind: db.EventKindPointsRedeem, RedemptionID: ev.ID},
		}, nil

	case helix.EventSubTypeChannelCustomPowerUpRedemptionAdd:
		ev, err := helix.ParseEventSubEvent[helix.ChannelCustomPowerUpRedemptionAddEvent](msg)
		if err != nil {
			return nil, err
		}
		viewerID, _ := strconv.Atoi(ev.UserID)
		return &redemption{
			broadcasterLogin: ev.BroadcasterUserLogin,
			viewerID:         viewerID,
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

	return nil, fmt.Errorf("no redemption in event type %q", msg.SubscriptionType)
}

func (s *Service) activeUser(login string) (*ingestUserConfig, bool) {
	s.activeUsersLock.RLock()
	defer s.activeUsersLock.RUnlock()
	userCfg, ok := s.activeUsers[strings.ToLower(login)]
	return userCfg, ok
}

func (s *Service) handleEventSub(ctx context.Context, msg *helix.EventSubWebhookMessage) {
	logger := s.logger.With("type", msg.SubscriptionType, "message_id", msg.MessageID)

	// Every chat message of every channel comes through here, so only the ones
	// worth a record are logged; everything else is logged whole.
	if msg.SubscriptionType != helix.EventSubTypeChannelChatMessage {
		logger.Info("eventsub notification", "subscription", msg.Subscription.ID, "event", string(msg.Event))
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	var err error
	switch msg.SubscriptionType {
	case helix.EventSubTypeChannelChatMessage:
		var ev *helix.ChannelChatMessageEvent
		if ev, err = helix.ParseEventSubEvent[helix.ChannelChatMessageEvent](msg); err == nil {
			line := chatMessageLine(ev)
			if line.rewardID != "" || line.cheered > 0 {
				logger.Info("eventsub notification", "subscription", msg.Subscription.ID, "event", string(msg.Event))
			}
			s.handleChatLine(line, feedEventSub)
		}

	case helix.EventSubTypeChannelChatNotification:
		var ev *helix.ChannelChatNotificationEvent
		if ev, err = helix.ParseEventSubEvent[helix.ChannelChatNotificationEvent](msg); err == nil {
			s.handleNotification(ctx, ev)
		}

	case helix.EventSubTypeChannelFollow:
		var ev *helix.ChannelFollowEvent
		if ev, err = helix.ParseEventSubEvent[helix.ChannelFollowEvent](msg); err == nil {
			s.handleFollow(ctx, ev, "eventsub:"+msg.MessageID)
		}

	case helix.EventSubTypeChannelPointsRedemptionAdd, helix.EventSubTypeChannelCustomPowerUpRedemptionAdd:
		var redeem *redemption
		if redeem, err = parseRedemption(msg); err == nil {
			s.attachRedemption(ctx, redeem)
		}

	default:
		err = errors.New("no intake for this event type")
	}

	if err != nil {
		logger.Error("failed to handle eventsub notification", "err", err, "event", string(msg.Event))
	}
}

func (s *Service) handleNotification(ctx context.Context, ev *helix.ChannelChatNotificationEvent) {
	userCfg, ok := s.activeUser(ev.BroadcasterUserLogin)
	if !ok {
		return
	}
	in, ok := notificationArrival(ev)
	if !ok {
		return
	}
	s.push(ctx, ev.BroadcasterUserLogin, userCfg, in, feedEventSub)
}

// Chat never announces a follow, so this is the one event with a single feed.
func (s *Service) handleFollow(ctx context.Context, ev *helix.ChannelFollowEvent, uniqueID string) {
	userCfg, ok := s.activeUser(ev.BroadcasterUserLogin)
	if !ok {
		return
	}
	viewerID, _ := strconv.Atoi(ev.UserID)
	s.push(ctx, ev.BroadcasterUserLogin, userCfg, arrival{
		msg:      db.TwitchMessage{TwitchLogin: ev.UserLogin, TwitchUserID: viewerID, Event: &db.EventMeta{Kind: db.EventKindFollow}},
		uniqueID: uniqueID,
	}, feedEventSub)
}

func (s *Service) attachRedemption(ctx context.Context, redeem *redemption) {
	userCfg, ok := s.activeUser(redeem.broadcasterLogin)
	if !ok {
		return
	}
	logger := s.logger.With("user", redeem.broadcasterLogin, "redemption", redeem.event.RedemptionID)

	queueID, err := s.db.AttachRedemption(ctx, userCfg.id, redeemKey(redeem.viewerID, redeem.rewardID, redeem.text), &redeem.event)
	switch {
	case errors.Is(err, db.ErrNoRows):
		// Its message is not queued: never sent (a reward without text), not ours
		// to play ("^^"), not arrived yet, or this redemption is attached already.
		logger.Warn("redemption has no message to go on")
	case err != nil:
		logger.Error("failed to attach redemption", "err", err)
	default:
		logger.Info("redemption attached", "queue_id", queueID)
	}
}
