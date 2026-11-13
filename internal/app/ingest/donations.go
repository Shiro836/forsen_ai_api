package ingest

import (
	"context"

	"app/db"
	"app/internal/app/donations"

	"github.com/google/uuid"
)

// syncDonations hands the donation manager the set of credentials that should
// be live. A failed token query leaves whatever is already connected alone.
func (s *Service) syncDonations(ctx context.Context, users []*db.IngestUser) {
	tokens, err := s.db.ListDonationProviderTokens(ctx)
	if err != nil {
		s.logger.Error("failed to list donation provider tokens", "err", err)
		return
	}
	s.donations.Sync(ctx, donationDesired(users, tokens))
}

func donationDesired(users []*db.IngestUser, tokens []db.DonationProviderToken) map[donations.Key]donations.Spec {
	allowed := make(map[uuid.UUID]struct{}, len(users))
	for _, u := range users {
		if u.Settings.LaneEnabled(db.MsgClassDonation) {
			allowed[u.ID] = struct{}{}
		}
	}

	desired := make(map[donations.Key]donations.Spec)
	for _, token := range tokens {
		if _, ok := allowed[token.UserID]; !ok {
			continue
		}
		desired[donations.Key{UserID: token.UserID, Provider: token.Provider}] = donations.Spec{
			Credentials: donations.Credentials{
				UserID:       token.UserID,
				AccessToken:  token.AccessToken,
				RefreshToken: token.RefreshToken,
				ExpiresAt:    token.ExpiresAt,
			},
			UpdatedAt: token.UpdatedAt,
		}
	}
	return desired
}
