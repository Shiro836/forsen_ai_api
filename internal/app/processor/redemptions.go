package processor

import (
	"context"
	"log/slog"
	"strconv"
	"time"

	"app/db"

	"github.com/google/uuid"
	"github.com/nicklaw5/helix/v2"
)

const (
	redemptionFulfilled = "FULFILLED"
	redemptionCanceled  = "CANCELED"
)

// closeRedemption settles a channel-point redemption on Twitch for streamers
// who asked for it: canceling refunds the viewer's points. It runs on its own
// goroutine because the next message must not wait for Twitch.
func (p *Processor) closeRedemption(ctx context.Context, logger *slog.Logger, broadcaster *db.User, msgID uuid.UUID, status string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)

	go func() {
		defer cancel()

		settings, err := p.db.GetUserSettings(ctx, broadcaster.ID)
		if err != nil {
			logger.Error("failed to get user settings for redemption", "msg_id", msgID, "err", err)
			return
		}
		if !settings.CloseRedemptions {
			return
		}

		// Read now rather than at claim time: the event half of a redemption can
		// land after chat's half was already claimed.
		msg, err := p.db.GetMessageByID(ctx, msgID)
		if err != nil {
			logger.Error("failed to get message for redemption", "msg_id", msgID, "err", err)
			return
		}
		event := msg.TwitchMessage.Event
		if event == nil || event.Kind != db.EventKindPointsRedeem || event.RedemptionID == "" {
			return
		}

		client, err := p.twitchClient.HelixForUser(ctx, logger, p.db, broadcaster)
		if err != nil {
			logger.Error("failed to get twitch client for redemption", "msg_id", msgID, "err", err)
			return
		}

		resp, err := client.UpdateChannelCustomRewardsRedemptionStatus(&helix.UpdateChannelCustomRewardsRedemptionStatusParams{
			ID:            event.RedemptionID,
			BroadcasterID: strconv.Itoa(broadcaster.TwitchUserID),
			RewardID:      msg.TwitchMessage.RewardID,
			Status:        status,
		})
		if err != nil {
			logger.Error("failed to update redemption", "msg_id", msgID, "status", status, "err", err)
			return
		}
		if resp.StatusCode > 299 {
			// Twitch lets an app settle only redemptions of rewards it created itself.
			logger.Warn("twitch refused to update redemption", "msg_id", msgID, "status", status, "code", resp.StatusCode, "message", resp.ErrorMessage)
			return
		}

		logger.Info("redemption updated", "msg_id", msgID, "status", status)
	}()
}
