package api

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	"app/db"
	"app/pkg/ai"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
)

func TestBuildSearchText(t *testing.T) {
	got := buildSearchText("Okayeg", "tresh", "  ShiroGopher  ", "forsen's\nfunny\tdota teammate", "")
	assert.Equal(t, "okayeg tresh shirogopher forsen's funny dota teammate", got)
}

func TestCharactersTemplateFilterAndLayout(t *testing.T) {
	now := time.Now()
	newCard := &db.Card{
		ID:               uuid.New(),
		Name:             "Mina Ha",
		Description:      "Ambitious vampire",
		ShortCharName:    sql.NullString{String: "mina", Valid: true},
		OwnerTwitchLogin: "stingray_57",
		Public:           true,
		Redeems:          84,
		TTSRedeems:       12,
		CreatedAt:        now.Add(-2 * 24 * time.Hour),
		TrendingScore:    18.5,
	}
	oldCard := &db.Card{
		ID:               uuid.New(),
		Name:             "Okayeg",
		Description:      "forsen's funny dota teammate",
		ShortCharName:    sql.NullString{String: "tresh", Valid: true},
		OwnerTwitchLogin: "shirogopher",
		Public:           true,
		Redeems:          2330,
		TTSRedeems:       2626,
		CreatedAt:        now.Add(-45 * 24 * time.Hour),
		TrendingScore:    42.0,
	}
	noVoiceCard := &db.Card{
		ID:               uuid.New(),
		Name:             "Astolfo (+18)",
		Description:      "wild",
		OwnerTwitchLogin: "sequally",
		Public:           true,
		CreatedAt:        now.Add(-20 * 24 * time.Hour),
	}

	makePage := func(isAdmin bool) *charsElem {
		return &charsElem{
			IsAdmin:       isAdmin,
			MineCount:     1,
			EnabledCount:  2,
			DisabledCount: 1,
			NewCount:      1,
			NoVoiceCount:  1,
			Characters: []*charElem{
				{
					Card:          newCard,
					CanEdit:       true,
					IsAdmin:       isAdmin,
					Author:        newCard.OwnerTwitchLogin,
					IsNew:         true,
					TotalRedeems:  96,
					TrendingScore: "18.5000",
					CreatedUnix:   newCard.CreatedAt.Unix(),
					SearchText:    buildSearchText(newCard.Name, newCard.ShortCharName.String, newCard.OwnerTwitchLogin, newCard.Description),
				},
				{
					Card:          oldCard,
					IsAdmin:       isAdmin,
					Author:        oldCard.OwnerTwitchLogin,
					Disabled:      true,
					TotalRedeems:  4956,
					TrendingScore: "42.0000",
					CreatedUnix:   oldCard.CreatedAt.Unix(),
					SearchText:    buildSearchText(oldCard.Name, oldCard.ShortCharName.String, oldCard.OwnerTwitchLogin, oldCard.Description),
				},
				{
					Card:          noVoiceCard,
					IsAdmin:       isAdmin,
					Author:        noVoiceCard.OwnerTwitchLogin,
					NeedsVoice:    true,
					TrendingScore: "0.0000",
					CreatedUnix:   noVoiceCard.CreatedAt.Unix(),
					SearchText:    buildSearchText(noVoiceCard.Name, "", noVoiceCard.OwnerTwitchLogin, noVoiceCard.Description),
				},
			},
		}
	}

	nonAdminHTML := string(getHtml("characters.html", makePage(false)))
	assert.Less(t, strings.Index(nonAdminHTML, "Universal TTS"), strings.Index(nonAdminHTML, `id="chars-search"`))
	assert.Less(t, strings.Index(nonAdminHTML, "Universal AI"), strings.Index(nonAdminHTML, `id="chars-search"`))
	assert.Less(t, strings.Index(nonAdminHTML, "Agent Baj"), strings.Index(nonAdminHTML, `id="chars-search"`))
	assert.Less(t, strings.Index(nonAdminHTML, `hx-get="/characters/new"`), strings.Index(nonAdminHTML, `id="chars-search"`))
	assert.Contains(t, nonAdminHTML, `<option value="newest" selected>Newest</option>`)
	assert.Contains(t, nonAdminHTML, `<option value="trending">Trending</option>`)
	assert.Contains(t, nonAdminHTML, `data-search="okayeg tresh shirogopher forsen&#39;s funny dota teammate"`)
	assert.Equal(t, 1, strings.Count(nonAdminHTML, ">NEW<"), "only cards within 14 days get NEW badge")
	assert.NotContains(t, nonAdminHTML, `data-char-scope="no-voice"`)

	adminHTML := string(getHtml("characters.html", makePage(true)))
	assert.Contains(t, adminHTML, `data-char-scope="no-voice"`)
}

func TestVoicesTemplateTrendingSearchAndNewBadge(t *testing.T) {
	page := &voicesListPage{
		NewCount: 1,
		Items: []voiceItem{
			{
				ID:            uuid.New(),
				Name:          "tresh",
				SearchText:    buildSearchText("tresh", "Okayeg", "forsen's funny dota teammate"),
				IsNew:         false,
				TrendingScore: "31.2500",
				Popularity:    4956,
				CreatedUnix:   time.Now().Add(-60 * 24 * time.Hour).Unix(),
			},
			{
				ID:            uuid.New(),
				Name:          "mina",
				SearchText:    buildSearchText("mina", "Mina Ha", "ambitious vampire"),
				IsNew:         true,
				TrendingScore: "19.5000",
				Popularity:    96,
				CreatedUnix:   time.Now().Add(-3 * 24 * time.Hour).Unix(),
			},
		},
		Sounds:   []soundItem{{ID: 137, Name: "vine boom"}},
		Filters:  []filterItem{{ID: 2, Name: "echo"}},
		Emotions: []string{"angry"},
		Melodies: []ai.Melody{{ID: "m1", Number: 1, Name: "Raw Meat"}},
	}

	out := string(getHtml("voices.html", page))

	// Original card name is searchable via data-search, but not shown on the card.
	assert.Contains(t, out, `data-search="tresh okayeg forsen&#39;s funny dota teammate"`)
	assert.NotContains(t, out, ">Okayeg<")
	assert.NotContains(t, out, ">Mina Ha<")
	assert.Contains(t, out, ">tresh<")
	assert.Contains(t, out, ">mina<")

	// Trending is the default active sort.
	assert.Contains(t, out, `data-voice-sort="trending"`)
	assert.Contains(t, out, `id="voices-new-only"`)
	assert.Equal(t, 1, strings.Count(out, ">NEW</span>\n\t\t\t\n\t\t\t<div"), "only 14-day voice card gets NEW badge")

	// Search inputs exist for sounds, filters, and melodies too.
	assert.Contains(t, out, `data-list-filter="sounds-list"`)
	assert.Contains(t, out, `data-list-filter="filters-list"`)
	assert.Contains(t, out, `data-list-filter="melodies-list"`)
}
