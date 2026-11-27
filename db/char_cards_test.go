package db

import (
	"testing"

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
