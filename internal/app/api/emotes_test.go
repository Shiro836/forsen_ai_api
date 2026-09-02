package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"app/db"
	"app/internal/emoteservice"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

func TestParseEmoteRef(t *testing.T) {
	t.Parallel()

	const id = "01J6KB8SBG00095HSJ1PN3XDPF"

	for _, in := range []string{
		id,
		"  " + id + "  ",
		"https://7tv.app/emotes/" + id,
		"https://7tv.app/emotes/" + id + "/",
		"https://7tv.app/emotes/" + id + "?tab=info",
		"https://old.7tv.app/emotes/" + id + "#top",
	} {
		got, err := parseEmoteRef(in)
		if err != nil {
			t.Fatalf("parseEmoteRef(%q): %v", in, err)
		}
		if got != id {
			t.Errorf("parseEmoteRef(%q) = %q, want %q", in, got, id)
		}
	}

	for _, bad := range []string{"", "   ", "https://7tv.app/emotes/", "short", "has spaces here", "../../etc/passwd", id + "!"} {
		if _, err := parseEmoteRef(bad); err == nil {
			t.Errorf("parseEmoteRef(%q) should have failed", bad)
		}
	}
}

func TestEmoteOriginalKey(t *testing.T) {
	t.Parallel()

	const id = "01J6KB8SBG00095HSJ1PN3XDPF"

	key, err := emoteOriginalKey("", id)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if key != "original/"+id+".webp" {
		t.Fatalf("unexpected key %q", key)
	}

	if _, err := emoteOriginalKey("bttv", id); err == nil {
		t.Error("unknown provider should be rejected")
	}
	for _, bad := range []string{"", "../../secret", "01J6KB8SBG00095HSJ1PN3XDPF/../x"} {
		if _, err := emoteOriginalKey("7tv", bad); err == nil {
			t.Errorf("emote id %q should be rejected", bad)
		}
	}
}

func TestVerdictReason(t *testing.T) {
	t.Parallel()

	if got := verdictReason(emoteservice.ReasonRule + ":abc123"); got != "blocked by rule abc123" {
		t.Errorf("rule reason = %q", got)
	}
	if got := verdictReason(emoteservice.ReasonUnclassified); got != "not classified yet" {
		t.Errorf("unclassified reason = %q", got)
	}
	if got := verdictReason("something new"); got != "something new" {
		t.Errorf("unknown reason should pass through, got %q", got)
	}
}

// The 7TV gate is two predicates: a channel's own streamer or any platform
// moderator may act on that channel, and only a platform moderator may act
// globally.
func TestEmoteChannelAllowed(t *testing.T) {
	t.Parallel()

	const (
		streamer = 100
		other    = 200
	)

	if !emoteChannelAllowed(streamer, streamer, false) {
		t.Error("a streamer must reach their own channel")
	}
	if emoteChannelAllowed(streamer, other, false) {
		t.Error("a streamer must not reach another channel")
	}
	if !emoteChannelAllowed(streamer, other, true) {
		t.Error("a platform moderator must reach any channel")
	}
}

func TestEmoteOverrideScope(t *testing.T) {
	t.Parallel()

	streamerID := uuid.New()
	scope := &emoteScope{Target: &db.User{ID: streamerID}}

	got, err := emoteOverrideScope(scope, "channel")
	if err != nil || got != streamerID.String() {
		t.Fatalf("channel scope = %q, %v", got, err)
	}

	if _, err := emoteOverrideScope(scope, "global"); err == nil {
		t.Fatal("a streamer without the moderator role must not reach the global lists")
	}

	scope.CanGlobal = true
	got, err = emoteOverrideScope(scope, "channel")
	if err != nil || got != streamerID.String() {
		t.Fatalf("moderator channel scope = %q, %v", got, err)
	}
	got, err = emoteOverrideScope(scope, "global")
	if err != nil || got != emoteservice.GlobalScope {
		t.Fatalf("moderator global scope = %q, %v", got, err)
	}

	// The channel-less global routes ignore whatever the form claimed.
	got, err = emoteOverrideScope(&emoteScope{CanGlobal: true}, "channel")
	if err != nil || got != emoteservice.GlobalScope {
		t.Fatalf("global-only scope = %q, %v", got, err)
	}
}

func TestParseEmoteSetRef(t *testing.T) {
	t.Parallel()

	const setID = "01HKQT8EWR000123456789ABCD"

	for in, want := range map[string]emoteSetRef{
		setID:                                 {SetID: setID},
		"https://7tv.app/emote-sets/" + setID: {SetID: setID},
		"https://7tv.app/emote-sets/" + setID + "/":  {SetID: setID},
		"https://twitch.tv/forsen":                   {TwitchLogin: "forsen"},
		"https://www.twitch.tv/forsen?referrer=raid": {TwitchLogin: "forsen"},
	} {
		got, err := parseEmoteSetRef(in)
		if err != nil {
			t.Fatalf("parseEmoteSetRef(%q): %v", in, err)
		}
		if got != want {
			t.Errorf("parseEmoteSetRef(%q) = %+v, want %+v", in, got, want)
		}
	}

	// A 7TV profile link ends in a user id, which is not a set id.
	for _, bad := range []string{"", "   ", "https://7tv.app/users/" + setID, "https://7tv.app/emotes/" + setID, "nope"} {
		if _, err := parseEmoteSetRef(bad); err == nil {
			t.Errorf("parseEmoteSetRef(%q) should have failed", bad)
		}
	}
}

// fakeEmoteService stands in for the emote service so the client's request
// shapes are checked without the real one running.
type fakeEmoteService struct {
	server  *httptest.Server
	tokens  []string
	methods map[string]string
}

func newFakeEmoteService(t *testing.T, routes map[string]any) *fakeEmoteService {
	t.Helper()

	f := &fakeEmoteService{methods: map[string]string{}}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.tokens = append(f.tokens, r.Header.Get(emoteservice.AuthHeader))
		f.methods[r.URL.Path] = r.Method

		body, ok := routes[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "not found"})

			return
		}
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(f.server.Close)

	return f
}

func (f *fakeEmoteService) client() *emoteClient {
	return &emoteClient{baseURL: f.server.URL, token: "secret", http: f.server.Client()}
}

func TestEmoteClientCalls(t *testing.T) {
	t.Parallel()

	f := newFakeEmoteService(t, map[string]any{
		"/v1/channel-set": map[string]any{
			"linked":   true,
			"username": "forsen",
			"emotes":   []map[string]any{{"emote_id": "abc", "name": "forsenE", "base_name": "forsenBase"}},
		},
		"/v1/verdicts": map[string]any{
			"verdicts": map[string]any{"abc": map[string]any{"emote_id": "abc", "verdict": "block", "reason": "nsfw"}},
		},
		"/v1/emotes": map[string]any{
			"emotes": []map[string]any{{"emote_id": "abc", "name": "forsenE", "provider": "7tv"}},
		},
		"/v1/global-set": map[string]any{
			"set_id": "global",
			"emotes": []map[string]any{{"emote_id": "abc", "name": "PogChamp"}},
		},
		"/v1/enqueue-set": map[string]any{"set_id": "s1", "emotes": 42, "queued": 40},
		"/v1/search": map[string]any{
			"results": []map[string]any{{"emote_id": "abc", "provider": "7tv", "name": "sadKanna", "score": 0.81}},
		},
		"/v1/queue": map[string]any{
			"statuses":            map[string]int{"pending": 3, "done": 1},
			"classifier_versions": map[string]int{"1": 484, "2": 12},
			"classifier_version":  2,
			"outdated":            484,
		},
		"/v1/rules": map[string]any{
			"rule":       map[string]any{"id": "r1", "rule_text": "feet"},
			"candidates": []map[string]any{{"emote_id": "abc", "name": "forsenE", "score": 0.42, "sources": []string{"image"}}},
			"rules":      []map[string]any{{"id": "r1", "rule_text": "feet", "status": "draft"}},
		},
	})
	c := f.client()
	ctx := context.Background()

	set, err := c.ChannelSet(ctx, 22484632)
	if err != nil {
		t.Fatalf("ChannelSet: %v", err)
	}
	if !set.Linked || len(set.Emotes) != 1 || set.Emotes[0].BaseName != "forsenBase" {
		t.Fatalf("unexpected channel set: %+v", set)
	}

	verdicts, err := c.Verdicts(ctx, "streamer", []string{"abc"})
	if err != nil {
		t.Fatalf("Verdicts: %v", err)
	}
	if verdicts["abc"].Verdict != emoteservice.VerdictBlock {
		t.Fatalf("unexpected verdicts: %+v", verdicts)
	}

	emotes, err := c.SearchEmotes(ctx, "forsen", 10)
	if err != nil || len(emotes) != 1 || emotes[0].ID != "abc" {
		t.Fatalf("SearchEmotes: %v %+v", err, emotes)
	}

	semantic, err := c.SemanticSearch(ctx, "crying anime girl", emoteSemanticLimit)
	if err != nil || len(semantic) != 1 || semantic[0].Name != "sadKanna" || semantic[0].Score != 0.81 {
		t.Fatalf("SemanticSearch: %v %+v", err, semantic)
	}

	global, err := c.GlobalSet(ctx)
	if err != nil || global.SetID != "global" || len(global.Emotes) != 1 {
		t.Fatalf("GlobalSet: %v %+v", err, global)
	}

	queuedSet, err := c.EnqueueSet(ctx, "s1", 0, emotePrioritySet)
	if err != nil || queuedSet.Emotes != 42 || queuedSet.Queued != 40 {
		t.Fatalf("EnqueueSet: %v %+v", err, queuedSet)
	}

	stats, err := c.QueueStats(ctx)
	if err != nil || stats.Statuses["pending"] != 3 || stats.Outdated != 484 || stats.ClassifierVersion != 2 {
		t.Fatalf("QueueStats: %v %+v", err, stats)
	}

	rules, err := c.ListRules(ctx, "streamer")
	if err != nil || len(rules) != 1 || rules[0].ID != "r1" {
		t.Fatalf("ListRules: %v %+v", err, rules)
	}

	preview, err := c.CreateRule(ctx, "streamer", "feet", nil)
	if err != nil || preview.Rule.ID != "r1" || len(preview.Candidates) != 1 {
		t.Fatalf("CreateRule: %v %+v", err, preview)
	}

	for _, token := range f.tokens {
		if token != "secret" {
			t.Fatalf("shared secret missing from a request, got %q", token)
		}
	}
	if f.methods["/v1/verdicts"] != http.MethodPost || f.methods["/v1/queue"] != http.MethodGet {
		t.Fatalf("unexpected methods: %+v", f.methods)
	}
}

func TestEmoteClientErrorCarriesStatus(t *testing.T) {
	t.Parallel()

	c := newFakeEmoteService(t, nil).client()

	err := c.DeleteOverride(context.Background(), emoteservice.Override{EmoteID: "abc"})

	var httpErr *emoteHTTPError
	if !errors.As(err, &httpErr) || httpErr.Status != http.StatusNotFound {
		t.Fatalf("expected a 404 emoteHTTPError, got %v", err)
	}
}

func TestEmoteCardTemplate(t *testing.T) {
	t.Parallel()

	card := emoteCard{
		Provider:    "7tv",
		EmoteID:     "01J6KB8SBG00095HSJ1PN3XDPF",
		Name:        "forsenCream",
		BaseName:    "forsenCreamBase",
		Aliased:     true,
		ImageURL:    emoteImageURL("7tv", "01J6KB8SBG00095HSJ1PN3XDPF"),
		Verdict:     "block",
		Reason:      "classified innuendo",
		ChannelPath: "/emotes/22484632",
	}

	rendered := getString("emote_card.html", card)
	for _, want := range []string{
		"/emote-image/7tv/01J6KB8SBG00095HSJ1PN3XDPF",
		`hx-post="/emotes/22484632/override"`,
		`value="channel"`,
		"alias of forsenCreamBase",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("emote card missing %q:\n%s", want, rendered)
		}
	}
	if strings.Contains(rendered, "cdn.7tv.app") {
		t.Fatal("emote card must never hotlink the 7TV CDN")
	}
	if strings.Contains(rendered, `value="global"`) {
		t.Fatal("global controls rendered for a streamer without the moderator role")
	}

	card.CanGlobal = true
	moderator := getString("emote_card.html", card)
	if !strings.Contains(moderator, `value="global"`) || !strings.Contains(moderator, `value="channel"`) {
		t.Fatalf("a moderator's channel card needs both scopes:\n%s", moderator)
	}

	// The global emote list has no channel, so only the global form belongs.
	card.ChannelPath = ""
	global := getString("emote_card.html", card)
	if !strings.Contains(global, `hx-post="/emotes/global/override"`) {
		t.Fatalf("global card missing its route:\n%s", global)
	}
	if strings.Contains(global, `value="channel"`) {
		t.Fatalf("global card must not carry channel controls:\n%s", global)
	}
}

func TestEmoteListTemplates(t *testing.T) {
	t.Parallel()

	card := emoteCard{
		Provider:    "7tv",
		EmoteID:     "01J6KB8SBG00095HSJ1PN3XDPF",
		Name:        "forsenCream",
		ImageURL:    emoteImageURL("7tv", "01J6KB8SBG00095HSJ1PN3XDPF"),
		Verdict:     "allow",
		Reason:      "auto-allowed",
		ChannelPath: "/emotes/22484632",
	}

	unlinked := getString("emote_channel.html", &emoteChannelList{})
	if !strings.Contains(unlinked, "no 7TV account") {
		t.Fatalf("unlinked channel notice missing:\n%s", unlinked)
	}

	lists := map[string]string{
		"emote_channel.html": getString("emote_channel.html", &emoteChannelList{Linked: true, Username: "forsen", Cards: []emoteCard{card}}),
		"emote_search.html":  getString("emote_search.html", &emoteSearchResult{Query: "forsen", Cards: []emoteCard{card}}),
		"emote_global.html":  getString("emote_global.html", &emoteGlobalList{SetID: "global", Cards: []emoteCard{card}}),
	}
	for name, out := range lists {
		if !strings.Contains(out, `hx-post="/emotes/22484632/override"`) {
			t.Fatalf("%s did not render emote cards:\n%s", name, out)
		}
	}
}

// One query, two answers: exact name matches and ranked contextual ones. The
// second half failing must not take the first half down with it.
func TestEmoteSearchTemplateRendersBothResultKinds(t *testing.T) {
	t.Parallel()

	named := emoteCard{EmoteID: "01J6KB8SBG00095HSJ1PN3XDPF", Name: "sadKanna", Verdict: "allow", ChannelPath: "/emotes/22484632"}
	contextual := emoteCard{EmoteID: "01HKQT8EWR000123456789ABCD", Name: "cryingWojak", Verdict: "allow", Score: "0.810", ChannelPath: "/emotes/22484632"}

	both := getString("emote_search.html", &emoteSearchResult{
		Query:      "crying anime girl",
		Cards:      []emoteCard{named},
		Contextual: []emoteCard{contextual},
	})
	for _, want := range []string{"matching by name", "Contextual matches", "sadKanna", "cryingWojak", "similarity 0.810"} {
		if !strings.Contains(both, want) {
			t.Fatalf("search results missing %q:\n%s", want, both)
		}
	}
	if strings.Count(both, "similarity 0.810") != 1 {
		t.Fatalf("the contextual card must carry its score exactly once:\n%s", both)
	}
	if out := getString("emote_card.html", named); strings.Contains(out, "similarity") {
		t.Fatalf("a name match has no similarity score to show:\n%s", out)
	}

	degraded := getString("emote_search.html", &emoteSearchResult{
		Query:         "crying anime girl",
		Cards:         []emoteCard{named},
		ContextualErr: "embedder not configured",
	})
	if !strings.Contains(degraded, "Contextual search is unavailable: embedder not configured") {
		t.Fatalf("embedder outage must be surfaced:\n%s", degraded)
	}
	if !strings.Contains(degraded, "sadKanna") {
		t.Fatalf("name results must survive an embedder outage:\n%s", degraded)
	}

	// An empty query lists the registry; there is nothing to embed.
	listing := getString("emote_search.html", &emoteSearchResult{Cards: []emoteCard{named}})
	if strings.Contains(listing, "Contextual matches") {
		t.Fatalf("an empty query must not claim a contextual section:\n%s", listing)
	}

	if out := getString("emote_queue.html", &emoteQueueStatus{
		Statuses: []emoteQueueEntry{{Status: "pending", Count: 5}}, Total: 5,
	}); !strings.Contains(out, "pending") || strings.Contains(out, "outdated version") {
		t.Fatalf("queue status wrong with nothing stale:\n%s", out)
	}

	if out := getString("emote_queue.html", &emoteQueueStatus{
		Statuses: []emoteQueueEntry{{Status: "pending", Count: 5}}, Total: 5, Outdated: 484, Version: 2,
	}); !strings.Contains(out, "classified with outdated version:") || !strings.Contains(out, "484") {
		t.Fatalf("stale count missing:\n%s", out)
	}

	if out := getString("emote_rules.html", &emoteRulesList{TwitchUserID: 22484632}); !strings.Contains(out, `hx-post="/emotes/22484632/rules"`) {
		t.Fatalf("rule form missing:\n%s", out)
	}
}

func TestEmotesMenuTemplate(t *testing.T) {
	t.Parallel()

	panels := []PanelData{{TwitchLogin: "forsen", TwitchUserID: 22484632}}

	streamer := getString("emotes_menu.html", &emotesMenu{Panels: panels})
	if !strings.Contains(streamer, `hx-get="/emotes/22484632"`) {
		t.Fatalf("menu link missing:\n%s", streamer)
	}

	// The classification queue is service-wide, so it belongs to the landing
	// page and not to any one channel.
	if !strings.Contains(streamer, `hx-get="/emotes/queue"`) {
		t.Fatalf("queue status missing from the landing page:\n%s", streamer)
	}
	for _, forbidden := range []string{"/emotes/global/set", "/emotes/global/enqueue-set"} {
		if strings.Contains(streamer, forbidden) {
			t.Fatalf("global section rendered for a plain streamer:\n%s", streamer)
		}
	}

	moderator := getString("emotes_menu.html", &emotesMenu{Panels: panels, CanGlobal: true})
	for _, want := range []string{`hx-get="/emotes/global/set"`, `hx-post="/emotes/global/enqueue-set"`, `hx-get="/emotes/global/search"`} {
		if !strings.Contains(moderator, want) {
			t.Fatalf("moderator menu missing %q:\n%s", want, moderator)
		}
	}
}

func TestEmotesPanelTemplate(t *testing.T) {
	t.Parallel()

	panel := getString("emotes.html", &emotesPanel{
		TwitchUserID: 22484632, TwitchLogin: "forsen", Available: true,
		Classes: classToggles([]string{emoteservice.ClassCum}),
	})
	for _, want := range []string{
		`hx-get="/emotes/22484632/channel"`,
		`hx-get="/emotes/22484632/search"`,
		`hx-get="/emotes/22484632/rules"`,
		`hx-post="/emotes/22484632/settings"`,
		`hx-post="/emotes/22484632/settings/reset"`,
		`hx-post="/emotes/22484632/enqueue"`,
		`hx-post="/emotes/22484632/enqueue-set"`,
	} {
		if !strings.Contains(panel, want) {
			t.Fatalf("panel missing %q:\n%s", want, panel)
		}
	}

	// The set listing runs to a thousand emotes, so it must come last and stay
	// collapsed until asked for.
	rules := strings.Index(panel, "/rules\"")
	channel := strings.Index(panel, "/channel\"")
	if rules < 0 || channel < 0 || rules > channel {
		t.Fatalf("rules must be rendered above the channel emote set:\n%s", panel)
	}
	if !strings.Contains(panel, "<details") || !strings.Contains(panel, `hx-trigger="click once"`) {
		t.Fatalf("channel set must be collapsed and load on demand:\n%s", panel)
	}
	if strings.Contains(panel, "/queue") {
		t.Fatalf("the service-wide queue must not sit on a channel page:\n%s", panel)
	}

	unavailable := getString("emotes.html", &emotesPanel{TwitchUserID: 1, TwitchLogin: "forsen"})
	if !strings.Contains(unavailable, "emote service is not configured") {
		t.Fatalf("unavailable notice missing:\n%s", unavailable)
	}
}

// The global routes sit under the same prefix as the channel ones, so a
// regression in route order would silently send /emotes/global/... to a handler
// that expects a numeric channel id.
func TestEmoteRoutePrecedence(t *testing.T) {
	t.Parallel()

	router := (&API{logger: slog.New(slog.NewTextHandler(io.Discard, nil)), cfg: &Config{}}).NewRouter()

	for _, tc := range []struct{ method, path, want string }{
		{http.MethodGet, "/emotes/queue", "/emotes/queue"},
		{http.MethodPost, "/emotes/global/platform-settings", "/emotes/global/platform-settings"},
		{http.MethodPost, "/emotes/global/reclassify", "/emotes/global/reclassify"},
		{http.MethodPost, "/emotes/22484632/settings/reset", "/emotes/{twitch_user_id}/settings/reset"},
		{http.MethodGet, "/emotes/global/set", "/emotes/global/set"},
		{http.MethodGet, "/emotes/global/search", "/emotes/global/search"},
		{http.MethodPost, "/emotes/global/override", "/emotes/global/override"},
		{http.MethodPost, "/emotes/global/enqueue-set", "/emotes/global/enqueue-set"},
		{http.MethodGet, "/emotes/22484632", "/emotes/{twitch_user_id}"},
		{http.MethodPost, "/emotes/22484632/enqueue-set", "/emotes/{twitch_user_id}/enqueue-set"},
		{http.MethodGet, "/emote-image/7tv/01J6KB8SBG00095HSJ1PN3XDPF", "/emote-image/{provider}/{id}"},
	} {
		rctx := chi.NewRouteContext()
		if !router.Match(rctx, tc.method, tc.path) {
			t.Fatalf("%s %s did not match any route", tc.method, tc.path)
		}
		if got := rctx.RoutePattern(); got != tc.want {
			t.Errorf("%s %s matched %q, want %q", tc.method, tc.path, got, tc.want)
		}
	}
}

func TestClassToggles(t *testing.T) {
	t.Parallel()

	got := classToggles([]string{emoteservice.ClassCum, emoteservice.ClassHate})
	if len(got) != len(emoteservice.ContentClasses) {
		t.Fatalf("every class needs a toggle, got %d", len(got))
	}

	for _, toggle := range got {
		if toggle.Label == "" {
			t.Errorf("class %q has no label", toggle.Class)
		}
		want := toggle.Class == emoteservice.ClassCum || toggle.Class == emoteservice.ClassHate
		if toggle.Blocked != want {
			t.Errorf("class %q blocked=%v, want %v", toggle.Class, toggle.Blocked, want)
		}
	}
}

func TestBlockedClassesFromForm(t *testing.T) {
	t.Parallel()

	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(
		"classes=cum&classes=hate&classes=cum&classes=notaclass"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if err := r.ParseForm(); err != nil {
		t.Fatal(err)
	}

	got := blockedClassesFromForm(r)
	if len(got) != 2 || got[0] != emoteservice.ClassCum || got[1] != emoteservice.ClassHate {
		t.Fatalf("a forged class must not survive the form: %v", got)
	}
}

func TestVerdictReasonNamesTheClass(t *testing.T) {
	t.Parallel()

	for _, class := range emoteservice.ContentClasses {
		if got := verdictReason(emoteservice.ReasonClass + ":" + class); got != "class: "+class {
			t.Errorf("reason for %q = %q", class, got)
		}
	}
}

func TestEmoteSettingsTemplate(t *testing.T) {
	t.Parallel()

	inherited := getString("emote_settings.html", &emoteSettingsForm{
		TwitchUserID: 22484632,
		Inherited:    true,
		Classes:      classToggles(emoteservice.DefaultBlockedClasses()),
	})
	for _, want := range []string{
		"Inherited from the platform default",
		`hx-post="/emotes/22484632/settings"`,
		`hx-post="/emotes/22484632/settings/reset"`,
	} {
		if !strings.Contains(inherited, want) {
			t.Fatalf("settings form missing %q:\n%s", want, inherited)
		}
	}
	for _, class := range emoteservice.ContentClasses {
		if !strings.Contains(inherited, `name="classes" value="`+class+`" class="mr-2 h-4 w-4" checked`) {
			t.Fatalf("class %q is not rendered as a checked toggle:\n%s", class, inherited)
		}
	}

	// A channel that chose for itself neither claims inheritance nor pre-ticks
	// the classes it turned off.
	own := getString("emote_settings.html", &emoteSettingsForm{
		TwitchUserID: 22484632,
		Classes:      classToggles([]string{emoteservice.ClassSexual}),
	})
	if strings.Contains(own, "Inherited from the platform default") {
		t.Fatalf("a materialized row must not claim inheritance:\n%s", own)
	}
	if strings.Count(own, "checked") != 1 {
		t.Fatalf("exactly one class should be ticked:\n%s", own)
	}
}

func TestEmotesMenuPlatformEditorIsAdminOnly(t *testing.T) {
	t.Parallel()

	moderator := getString("emotes_menu.html", &emotesMenu{CanGlobal: true})
	for _, forbidden := range []string{"/emotes/global/platform-settings", "/emotes/global/reclassify"} {
		if strings.Contains(moderator, forbidden) {
			t.Fatalf("a moderator must not see the platform editor:\n%s", moderator)
		}
	}

	admin := getString("emotes_menu.html", &emotesMenu{
		CanGlobal:       true,
		IsAdmin:         true,
		PlatformClasses: classToggles(emoteservice.DefaultBlockedClasses()),
	})
	for _, want := range []string{
		`hx-post="/emotes/global/platform-settings"`,
		`hx-post="/emotes/global/reclassify"`,
		"Platform default blocked classes",
	} {
		if !strings.Contains(admin, want) {
			t.Fatalf("admin menu missing %q:\n%s", want, admin)
		}
	}
}

func TestNavbarSevenTVTab(t *testing.T) {
	t.Parallel()

	nav := getString("navbar.html", &navPage{IsAuthenticated: true})
	for _, want := range []string{`data-path="/emotes"`, ">7TV<", ">beta<"} {
		if !strings.Contains(nav, want) {
			t.Fatalf("navbar missing %q:\n%s", want, nav)
		}
	}
}

func TestRenderRulePreview(t *testing.T) {
	t.Parallel()

	rendered := string(renderRulePreview(22484632, &rulePreview{
		Rule: &emoteservice.Rule{ID: "r1", RuleText: "feet"},
		Candidates: []emoteservice.Candidate{{
			Provider: "7tv",
			EmoteID:  "01J6KB8SBG00095HSJ1PN3XDPF",
			Name:     "forsenFeet",
			Sources:  []string{"image", "description"},
			Score:    0.4213,
		}},
	}))

	for _, want := range []string{
		`hx-post="/emotes/22484632/rules/r1/confirm"`,
		`name="emote_ids" value="01J6KB8SBG00095HSJ1PN3XDPF"`,
		"/emote-image/7tv/01J6KB8SBG00095HSJ1PN3XDPF",
		"0.421",
		"image, description",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rule preview missing %q:\n%s", want, rendered)
		}
	}

	failed := string(renderRulePreview(1, &rulePreview{
		Rule:         &emoteservice.Rule{ID: "r1", RuleText: "feet"},
		PreviewError: "embedder not configured",
	}))
	if !strings.Contains(failed, "embedder not configured") {
		t.Fatalf("preview error not surfaced:\n%s", failed)
	}
}
