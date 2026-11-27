package api

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"app/db"
	"app/pkg/ai"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildSearchText(t *testing.T) {
	got := buildSearchText("Okayeg", "tresh", "  ShiroGopher  ", "forsen's\nfunny\tdota teammate", "")
	assert.Equal(t, "okayeg tresh shirogopher forsen's funny dota teammate", got)
}

func TestFilterAndSortChars(t *testing.T) {
	now := time.Now()
	all := []*charElem{
		{
			Card:         &db.Card{ID: uuid.New(), Name: "Mina Ha", Redeems: 84, TTSRedeems: 12, TrendingScore: 51.6, CreatedAt: now.Add(-2 * 24 * time.Hour)},
			CanEdit:      true,
			Author:       "stingray_57",
			IsNew:        true,
			TotalRedeems: 96,
			SearchText:   buildSearchText("Mina Ha", "mina", "stingray_57", "ambitious vampire"),
		},
		{
			Card:         &db.Card{ID: uuid.New(), Name: "Okayeg", Redeems: 2330, TTSRedeems: 2626, TrendingScore: 30.0, CreatedAt: now.Add(-60 * 24 * time.Hour)},
			Author:       "shirogopher",
			Disabled:     true,
			TotalRedeems: 4956,
			SearchText:   buildSearchText("Okayeg", "tresh", "shirogopher", "forsen's funny dota teammate"),
		},
		{
			Card:       &db.Card{ID: uuid.New(), Name: "Astolfo (+18)", Redeems: 249, TTSRedeems: 13, CreatedAt: now.Add(-20 * 24 * time.Hour)},
			Author:     "sequally",
			NeedsVoice: true,
			SearchText: buildSearchText("Astolfo (+18)", "", "sequally", "wild"),
		},
	}

	got := filterAndSortChars(all, "tresh", "newest", "all")
	require.Len(t, got, 1)
	assert.Equal(t, "Okayeg", got[0].Card.Name)

	got = filterAndSortChars(all, "", "trending", "all")
	require.Len(t, got, 3)
	assert.Equal(t, "Mina Ha", got[0].Card.Name)
	assert.Equal(t, "Okayeg", got[1].Card.Name)

	assert.Len(t, filterAndSortChars(all, "", "newest", "mine"), 1)
	assert.Len(t, filterAndSortChars(all, "", "newest", "disabled"), 1)
	assert.Len(t, filterAndSortChars(all, "", "newest", "enabled"), 2)
	assert.Len(t, filterAndSortChars(all, "", "newest", "new"), 1)
	assert.Len(t, filterAndSortChars(all, "", "newest", "no-voice"), 1)
}

func TestCharactersTemplateHTMXNoScript(t *testing.T) {
	now := time.Now()
	newCard := &db.Card{
		ID:               uuid.New(),
		Name:             "Mina Ha",
		ShortCharName:    sql.NullString{String: "mina", Valid: true},
		OwnerTwitchLogin: "stingray_57",
		Public:           true,
		CreatedAt:        now.Add(-2 * 24 * time.Hour),
	}
	oldCard := &db.Card{
		ID:               uuid.New(),
		Name:             "Okayeg",
		ShortCharName:    sql.NullString{String: "tresh", Valid: true},
		OwnerTwitchLogin: "shirogopher",
		Public:           true,
		CreatedAt:        now.Add(-45 * 24 * time.Hour),
	}

	makePage := func(isAdmin bool) *charsElem {
		return &charsElem{
			IsAdmin:       isAdmin,
			Sort:          "newest",
			Scope:         "all",
			TotalCount:    2,
			MineCount:     1,
			EnabledCount:  1,
			DisabledCount: 1,
			NewCount:      1,
			NoVoiceCount:  1,
			Characters: []*charElem{
				{Card: newCard, CanEdit: true, IsAdmin: isAdmin, Author: newCard.OwnerTwitchLogin, IsNew: true},
				{Card: oldCard, IsAdmin: isAdmin, Author: oldCard.OwnerTwitchLogin, Disabled: true},
			},
		}
	}

	nonAdminHTML := string(getHtml("characters.html", makePage(false)))
	assert.NotContains(t, nonAdminHTML, "<script")
	assert.Less(t, strings.Index(nonAdminHTML, "Universal TTS"), strings.Index(nonAdminHTML, `id="chars-filters"`))
	assert.Less(t, strings.Index(nonAdminHTML, "Universal AI"), strings.Index(nonAdminHTML, `id="chars-filters"`))
	assert.Less(t, strings.Index(nonAdminHTML, "Agent Baj"), strings.Index(nonAdminHTML, `id="chars-filters"`))
	assert.Less(t, strings.Index(nonAdminHTML, `hx-get="/characters/new"`), strings.Index(nonAdminHTML, `id="chars-filters"`))
	assert.Contains(t, nonAdminHTML, `hx-get="/characters/list"`)
	assert.Contains(t, nonAdminHTML, `<option value="newest" selected>Newest</option>`)
	assert.Contains(t, nonAdminHTML, `<option value="trending">Trending</option>`)
	assert.Equal(t, 1, strings.Count(nonAdminHTML, ">NEW<"))
	assert.NotContains(t, nonAdminHTML, `value="no-voice"`)

	adminHTML := string(getHtml("characters.html", makePage(true)))
	assert.Contains(t, adminHTML, `value="no-voice"`)
}

func TestVoicesHTMXFilterAndOriginalNameHidden(t *testing.T) {
	now := time.Now()
	raw := []db.PublicShortName{
		{
			ID:            uuid.New(),
			ShortCharName: "tresh",
			Name:          "Okayeg",
			Description:   "forsen's funny dota teammate",
			Redeems:       2330,
			TTSRedeems:    2626,
			CreatedAt:     now.Add(-60 * 24 * time.Hour),
			TrendingScore: 30.0,
		},
		{
			ID:            uuid.New(),
			ShortCharName: "mina",
			Name:          "Mina Ha",
			Description:   "ambitious vampire",
			Redeems:       84,
			TTSRedeems:    12,
			CreatedAt:     now.Add(-3 * 24 * time.Hour),
			TrendingScore: 51.6,
		},
	}

	// Default sort is Trending (mina first) and original card names are not rendered.
	req := httptest.NewRequest(http.MethodGet, "/voices", nil)
	page := buildVoicesPage(req, raw, "/voices", "/voices/list/cards", []ai.Melody{{ID: "m1", Number: 1, Name: "Raw Meat"}})
	require.Len(t, page.Items, 2)
	assert.Equal(t, "mina", page.Items[0].Name)
	assert.Equal(t, "tresh", page.Items[1].Name)

	out := string(getHtml("voices.html", page))
	assert.NotContains(t, out, "Okayeg")
	assert.NotContains(t, out, "Mina Ha")
	assert.Contains(t, out, `data-clipboard-text="tresh: "`)
	assert.Contains(t, out, `hx-get="/voices/list/cards"`)
	assert.Contains(t, out, `hx-get="/voices/list/sounds"`)
	assert.Contains(t, out, `hx-get="/voices/list/filters"`)
	assert.Contains(t, out, `hx-get="/voices/list/melodies"`)
	assert.Contains(t, out, `value="trending" class="peer sr-only" checked`)
	assert.Equal(t, 1, strings.Count(out, ">NEW</span>\n\t\t\t\n\t\t\t<div"))

	// Search finds through original card name even though it isn't shown on the card.
	searchReq := httptest.NewRequest(http.MethodGet, "/voices/list/cards?q=okayeg", nil)
	filtered := buildVoicesPage(searchReq, raw, "/voices", "/voices/list/cards", nil)
	require.Len(t, filtered.Items, 1)
	assert.Equal(t, "tresh", filtered.Items[0].Name)

	// New-only filter keeps only cards released within 14 days.
	newReq := httptest.NewRequest(http.MethodGet, "/voices/list/cards?new=1", nil)
	onlyNew := buildVoicesPage(newReq, raw, "/voices", "/voices/list/cards", nil)
	require.Len(t, onlyNew.Items, 1)
	assert.Equal(t, "mina", onlyNew.Items[0].Name)

	// Sounds and filters HTMX endpoints filter server-side.
	a := &API{}
	wSounds := httptest.NewRecorder()
	a.voicesListSounds(wSounds, httptest.NewRequest(http.MethodGet, "/voices/list/sounds?q=137", nil))
	assert.Equal(t, http.StatusOK, wSounds.Code)
	assert.Contains(t, wSounds.Body.String(), "[137]")

	wFilters := httptest.NewRecorder()
	a.voicesListFilters(wFilters, httptest.NewRequest(http.MethodGet, "/voices/list/filters?q=echo", nil))
	assert.Equal(t, http.StatusOK, wFilters.Code)
	assert.Contains(t, strings.ToLower(wFilters.Body.String()), "echo")
}
