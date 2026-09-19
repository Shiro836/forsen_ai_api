package api

import (
	"net/http"
	"net/url"
	"testing"

	"app/db"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBDPresetsAreSaveable(t *testing.T) {
	require.Equal(t, []string{"default", "cute", "gachi", "forsen"}, bdPresetNames())

	for _, preset := range bdPresets {
		for _, fields := range eventCardLines {
			for _, field := range fields {
				text, ok := preset.Lines[field.Line]
				require.Truef(t, ok, "%s preset has no %s line", preset.Name, field.Line)
				assert.NoErrorf(t, (&db.UserSettings{}).SetEventLine(field.Line, text), "%s preset, %s line", preset.Name, field.Line)
			}
		}
	}
}

func bdForm(state *bdState, extra url.Values) *http.Request {
	form := url.Values{"order": {formatBDOrder(state.Order)}}
	for class, enabled := range state.Enabled {
		if enabled {
			form.Add("on", string(class))
		}
	}
	for class, action := range state.Actions {
		form.Set("action_"+string(class), "2")
		if action.CardID != nil {
			form.Set("card_"+string(class), action.CardID.String())
		}
	}
	for line, text := range state.Lines {
		form.Set("line_"+string(line), text)
	}
	if !state.FollowsStay {
		form.Set("follows_yield", "on")
	}
	for key, values := range extra {
		form[key] = values
	}
	return &http.Request{Form: form}
}

func TestBDStateSurvivesTheForm(t *testing.T) {
	stored := bdStateFromSettings(&db.UserSettings{})

	state, err := bdStateFromForm(bdForm(stored, nil))
	require.NoError(t, err)
	assert.Equal(t, stored, state, "an untouched form must come back as it was rendered")

	_, err = bdStateFromForm(bdForm(stored, url.Values{"order": {"reward,bits"}}))
	assert.Error(t, err, "an order that drops lanes must be refused")
}

func TestBDFollowsYieldByDefault(t *testing.T) {
	stored := bdStateFromSettings(&db.UserSettings{})
	assert.False(t, stored.FollowsStay)

	page := getString("bd-event", &bdEvent{Class: db.MsgClassFollow, CanYield: true, Yields: true})
	assert.Contains(t, page, `name="follows_yield" class="h-4 w-4" checked`)
	page = getString("bd-event", &bdEvent{Class: db.MsgClassFollow, CanYield: true})
	assert.NotContains(t, page, "checked>", "an unticked box must render unticked")

	form := bdForm(stored, nil)
	form.Form.Del("follows_yield")
	unticked, err := bdStateFromForm(form)
	require.NoError(t, err)
	assert.True(t, unticked.FollowsStay)

	settings := &db.UserSettings{}
	errs := (&API{}).bdStore(form, nil, settings, unticked)
	assert.False(t, errs.any())
	assert.True(t, settings.FollowsStay)
}

func TestBDEdit(t *testing.T) {
	stored := bdStateFromSettings(&db.UserSettings{})

	r := bdForm(stored, url.Values{"op": {"preset"}, "class": {"sub"}, "preset": {"gachi"}})
	state, err := bdStateFromForm(r)
	require.NoError(t, err)
	_, err = state.edit(r)
	require.NoError(t, err)

	gachi := bdPresetByName("gachi")
	assert.Equal(t, gachi.Lines[db.EventLineResub], state.Lines[db.EventLineResub])
	assert.Equal(t, db.EventLineRaid.Default(), state.Lines[db.EventLineRaid], "a preset fills only its own card")

	r = bdForm(stored, url.Values{"op": {"split"}, "group": {"0"}})
	state, err = bdStateFromForm(r)
	require.NoError(t, err)
	_, err = state.edit(r)
	require.NoError(t, err)
	assert.Equal(t, "donation,bits,sub,reward,raid,streak,follow", formatBDOrder(state.Order))

	r = bdForm(stored, url.Values{"op": {"merge"}, "group": {"1"}})
	state, err = bdStateFromForm(r)
	require.NoError(t, err)
	_, err = state.edit(r)
	assert.Error(t, err, "only paid lanes merge")
}
