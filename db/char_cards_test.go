package db

import (
	"math"
	"testing"
	"time"

	sq "github.com/Masterminds/squirrel"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRecentStatsJoinSquirrelPlaceholders(t *testing.T) {
	joinSQL, joinArgs := recentStatsJoin()
	userID := uuid.New()

	query, args, err := psql.Select("cc.id", "coalesce(r.score, 0)::double precision").
		From("char_cards cc").
		LeftJoin(joinSQL, joinArgs...).
		Where(sq.Or{sq.Eq{"cc.owner_user_id": userID}, sq.Eq{"cc.public": true}}).
		ToSql()
	require.NoError(t, err)
	assert.Contains(t, query, "$1::double precision")
	assert.Contains(t, query, "$2::int - 1")
	assert.Contains(t, query, "cc.owner_user_id = $3")
	assert.Contains(t, query, "cc.public = $4")
	require.Len(t, args, 4)
	assert.Equal(t, trendingHalfLifeDays, args[0])
	assert.Equal(t, trendingWindowDays, args[1])
	assert.Equal(t, userID.String(), args[2])
	assert.Equal(t, true, args[3])
}

func TestApplyVoiceFreshnessSituations(t *testing.T) {
	now := time.Date(2026, 11, 27, 17, 0, 0, 0, time.UTC)

	t.Run("cold-start brand new voice lands below evergreens but well above dead cards", func(t *testing.T) {
		voices := []PublicShortName{
			{ShortCharName: "obiwan", TrendingScore: 500, Redeems: 5000, CreatedAt: now.Add(-300 * 24 * time.Hour)},
			{ShortCharName: "cancer", TrendingScore: 280, Redeems: 9000, CreatedAt: now.Add(-500 * 24 * time.Hour)},
			{ShortCharName: "forsen", TrendingScore: 55, Redeems: 9800, CreatedAt: now.Add(-800 * 24 * time.Hour)},
			{ShortCharName: "trump", TrendingScore: 33, Redeems: 2300, CreatedAt: now.Add(-800 * 24 * time.Hour)},
			{ShortCharName: "doc", TrendingScore: 22, Redeems: 2800, CreatedAt: now.Add(-600 * 24 * time.Hour)},
			{ShortCharName: "sheikh", TrendingScore: 17, Redeems: 4200, CreatedAt: now.Add(-800 * 24 * time.Hour)},
			{ShortCharName: "udisen", TrendingScore: 12, Redeems: 2200, CreatedAt: now.Add(-700 * 24 * time.Hour)},
			{ShortCharName: "jinx", TrendingScore: 0, Redeems: 163, CreatedAt: now.Add(-200 * 24 * time.Hour)},
			{ShortCharName: "wesker", TrendingScore: 0, Redeems: 9, CreatedAt: now.Add(-60 * 24 * time.Hour)},
			{ShortCharName: "acheron", TrendingScore: 0, Redeems: 0, CreatedAt: now.Add(-6 * time.Hour)},
			{ShortCharName: "mina", TrendingScore: 50, Redeems: 96, CreatedAt: now.Add(-2 * 24 * time.Hour)},
			{ShortCharName: "baba", TrendingScore: 2, Redeems: 20, CreatedAt: now.Add(-10 * 24 * time.Hour)},
		}

		applyVoiceFreshness(voices, now)

		rankOf := func(name string) int {
			for i, v := range voices {
				if v.ShortCharName == name {
					return i + 1
				}
			}
			return -1
		}

		assert.Equal(t, 1, rankOf("obiwan"))
		assert.Equal(t, 2, rankOf("cancer"))
		// Hot new voice climbs ahead of older medium-hot staples.
		assert.Less(t, rankOf("mina"), rankOf("forsen"))
		// Brand new 0-redeem voice surfaces near the top instead of dead last,
		// without displacing the top evergreens.
		assert.Greater(t, rankOf("acheron"), rankOf("cancer"))
		assert.Less(t, rankOf("acheron"), rankOf("udisen"))
		assert.Less(t, rankOf("acheron"), rankOf("jinx"))
		// 10-day-old voice with low usage decays well below brand new and active voices.
		assert.Greater(t, rankOf("baba"), rankOf("acheron"))
	})

	t.Run("freshness decays to zero after 14 days", func(t *testing.T) {
		assert.Equal(t, 0.0, freshnessBoost(now, now.Add(-15*24*time.Hour), 30.0))
		half := freshnessBoost(now, now.Add(-3*24*time.Hour), 30.0)
		assert.InDelta(t, 15.0, half, 0.01)
	})

	t.Run("empty daily stats still orders brand-new releases first", func(t *testing.T) {
		voices := []PublicShortName{
			{ShortCharName: "old_big", TrendingScore: 0, Redeems: 5000, CreatedAt: now.Add(-200 * 24 * time.Hour)},
			{ShortCharName: "brand_new", TrendingScore: 0, Redeems: 0, CreatedAt: now.Add(-2 * time.Hour)},
			{ShortCharName: "week_old", TrendingScore: 0, Redeems: 0, CreatedAt: now.Add(-7 * 24 * time.Hour)},
		}
		applyVoiceFreshness(voices, now)
		assert.Equal(t, "brand_new", voices[0].ShortCharName)
		assert.Equal(t, "week_old", voices[1].ShortCharName)
		assert.Equal(t, "old_big", voices[2].ShortCharName)
	})

	t.Run("scale-adaptive across 10x traffic", func(t *testing.T) {
		makeRoster := func(mult float64) []PublicShortName {
			out := make([]PublicShortName, 0, 21)
			for i := 1; i <= 20; i++ {
				out = append(out, PublicShortName{
					ShortCharName: "old",
					TrendingScore: float64(i) * mult,
					CreatedAt:     now.Add(-100 * 24 * time.Hour),
				})
			}
			out = append(out, PublicShortName{
				ShortCharName: "fresh",
				TrendingScore: 0,
				CreatedAt:     now.Add(-1 * time.Hour),
			})
			applyVoiceFreshness(out, now)
			return out
		}

		low := makeRoster(1.0)
		high := makeRoster(10.0)
		rankFresh := func(list []PublicShortName) int {
			for i, v := range list {
				if v.ShortCharName == "fresh" {
					return i + 1
				}
			}
			return -1
		}
		assert.Equal(t, rankFresh(low), rankFresh(high), "new voice lands at the same rank regardless of traffic scale")
		assert.True(t, math.Abs(float64(rankFresh(low)-3)) <= 1)
	})
}
