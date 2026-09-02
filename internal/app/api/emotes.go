package api

import (
	"errors"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"app/db"
	"app/internal/emoteservice"
	"app/pkg/ctxstore"
	"app/pkg/s3client"

	"github.com/go-chi/chi/v5"
	"github.com/nicklaw5/helix/v2"
)

// emoteIDPattern is deliberately loose because 7TV has shipped both 24-char
// ObjectIDs and 26-char ULIDs. It exists to keep separators out of ids that
// become S3 object keys, not to validate the id shape.
var emoteIDPattern = regexp.MustCompile(`^[0-9A-Za-z]{16,40}$`)

var twitchLoginPattern = regexp.MustCompile(`^[0-9A-Za-z_]{3,25}$`)

// Registry page bounds. Both sections render every hit as an image, so the
// contextual half is kept smaller — it has no relevance floor to thin it out.
const (
	emoteSearchLimit   = 60
	emoteSemanticLimit = 40
)

// Classification priorities. A human is waiting on a single pasted emote; a
// whole set is bulk, but still ahead of the background seed crawl at 0.
const (
	emotePriorityManual = 100
	emotePrioritySet    = 50
)

// trimRefURL strips a link down to its last path segment, leaving a bare id
// untouched.
func trimRefURL(raw string) string {
	ref := strings.TrimSpace(raw)
	if i := strings.IndexAny(ref, "?#"); i >= 0 {
		ref = ref[:i]
	}
	ref = strings.TrimRight(ref, "/")
	if i := strings.LastIndex(ref, "/"); i >= 0 {
		ref = ref[i+1:]
	}

	return ref
}

// parseEmoteRef accepts a 7tv.app emote link or a bare emote id and returns the
// id.
func parseEmoteRef(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", fmt.Errorf("empty emote link")
	}

	ref := trimRefURL(raw)
	if !emoteIDPattern.MatchString(ref) {
		return "", fmt.Errorf("%q is not a 7tv emote link or id", raw)
	}

	return ref, nil
}

// emoteSetRef is what a classify-set request pointed at: a 7TV emote set, or a
// Twitch channel whose active set has to be looked up first.
type emoteSetRef struct {
	SetID       string
	TwitchLogin string
}

// parseEmoteSetRef accepts a 7tv.app emote-set link, a bare set id, or a
// twitch.tv channel link. A 7TV profile link is rejected on purpose: its last
// segment is a 7TV user id, which is not a set id and would silently classify
// the wrong emotes.
func parseEmoteSetRef(raw string) (emoteSetRef, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return emoteSetRef{}, fmt.Errorf("empty emote set link")
	}

	ref := trimRefURL(trimmed)

	if strings.Contains(trimmed, "twitch.tv/") {
		if !twitchLoginPattern.MatchString(ref) {
			return emoteSetRef{}, fmt.Errorf("%q is not a twitch channel link", raw)
		}

		return emoteSetRef{TwitchLogin: ref}, nil
	}

	if strings.Contains(trimmed, "/") && !strings.Contains(trimmed, "/emote-sets/") {
		return emoteSetRef{}, fmt.Errorf("%q is not an emote set link; paste the /emote-sets/ url or a twitch.tv channel link", raw)
	}
	if !emoteIDPattern.MatchString(ref) {
		return emoteSetRef{}, fmt.Errorf("%q is not a 7tv emote set link or id", raw)
	}

	return emoteSetRef{SetID: ref}, nil
}

// emoteOriginalKey mirrors the object key the emote service's cache builder
// writes. The main app has no access to that service's database, so the key is
// derived rather than looked up.
func emoteOriginalKey(provider, emoteID string) (string, error) {
	if emoteservice.NormalizeProvider(provider) != emoteservice.ProviderSevenTV {
		return "", fmt.Errorf("unknown emote provider %q", provider)
	}
	if !emoteIDPattern.MatchString(emoteID) {
		return "", fmt.Errorf("invalid emote id")
	}

	return "original/" + emoteID + ".webp", nil
}

// emoteImage streams an emote's cached original out of our own S3. 7TV's CDN is
// ECH-blocked in some countries, so neither the overlay nor this panel may
// hotlink it. Objects are immutable, hence the year-long cache.
func (api *API) emoteImage(w http.ResponseWriter, r *http.Request) {
	key, err := emoteOriginalKey(chi.URLParam(r, "provider"), chi.URLParam(r, "id"))
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(err.Error()))

		return
	}

	obj, err := api.s3.GetObject(r.Context(), s3client.EmotesBucket, key)
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("not found"))

		return
	}
	defer obj.Close()

	data, err := io.ReadAll(obj)
	if err != nil {
		// minio reports a missing object on the first read, not on GetObject, so
		// "not cached yet" and a broken bucket both land here — the log is what
		// tells them apart.
		api.logger.Info("emote image unavailable", "key", key, "err", err)
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("not found"))

		return
	}

	w.Header().Set("Content-Type", "image/webp")
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	_, _ = w.Write(data)
}

// emoteScope is the channel a /emotes request acts on, plus whether the caller
// also holds the platform moderator role. Target is nil in the global scope,
// which belongs to no channel. Reaching a scope at all is the authorization:
// the resolvers refuse before returning one.
type emoteScope struct {
	Target    *db.User
	Caller    *db.User
	CanGlobal bool
	IsAdmin   bool
}

// StreamerID is what the emote service keys per-channel lists on; the global
// lists carry the nil-uuid scope instead.
func (s *emoteScope) StreamerID() string {
	if s.Target == nil {
		return emoteservice.GlobalScope
	}

	return s.Target.ID.String()
}

// ChannelPath is the route prefix for this scope's per-channel actions, empty
// when there is no channel.
func (s *emoteScope) ChannelPath() string {
	if s.Target == nil {
		return ""
	}

	return "/emotes/" + strconv.Itoa(s.Target.TwitchUserID)
}

// canModerateEmotes reports the platform moderator role. It is what lets a user
// act on channels other than their own and on the global lists; per-channel
// trust deliberately does not carry over into either.
func (api *API) canModerateEmotes(r *http.Request, user *db.User) (bool, error) {
	for _, permission := range []db.Permission{db.PermissionMod, db.PermissionAdmin} {
		has, _, err := api.db.HasPermission(r.Context(), user.TwitchUserID, permission)
		if err != nil {
			return false, fmt.Errorf("failed to check %s permission: %w", permission, err)
		}
		if has {
			return true, nil
		}
	}

	return false, nil
}

// emoteChannelAllowed decides who may act on one channel's 7TV page: the
// streamer whose channel it is, or a platform moderator on any channel.
func emoteChannelAllowed(callerTwitchUserID, targetTwitchUserID int, canGlobal bool) bool {
	return canGlobal || callerTwitchUserID == targetTwitchUserID
}

// resolveEmoteScope authorizes one channel's 7TV page and everything on it.
func (api *API) resolveEmoteScope(r *http.Request) (*emoteScope, error) {
	user := ctxstore.GetUser(r.Context())
	if user == nil {
		return nil, fmt.Errorf("unauthorized")
	}

	targetTwitchUserID, err := getTwitchUserID(r)
	if err != nil {
		return nil, fmt.Errorf("failed to get twitch user id: %w", err)
	}

	canGlobal, err := api.canModerateEmotes(r, user)
	if err != nil {
		return nil, err
	}
	if !emoteChannelAllowed(user.TwitchUserID, targetTwitchUserID, canGlobal) {
		return nil, fmt.Errorf("you can only manage your own channel's 7TV emotes")
	}

	target, err := api.db.GetUserByTwitchUserID(r.Context(), targetTwitchUserID)
	if err != nil {
		return nil, fmt.Errorf("failed to get target user: %w", err)
	}

	return &emoteScope{Target: target, Caller: user, CanGlobal: canGlobal}, nil
}

// resolveGlobalScope authorizes the channel-less routes — the global emote
// lists and whole-set classification — which only platform moderators may use.
func (api *API) resolveGlobalScope(r *http.Request) (*emoteScope, error) {
	user := ctxstore.GetUser(r.Context())
	if user == nil {
		return nil, fmt.Errorf("unauthorized")
	}

	canGlobal, err := api.canModerateEmotes(r, user)
	if err != nil {
		return nil, err
	}
	if !canGlobal {
		return nil, fmt.Errorf("global 7TV emotes are restricted to platform moderators")
	}

	return &emoteScope{Caller: user, CanGlobal: true}, nil
}

// resolveAdminScope guards the platform-wide knobs — the inherited default and
// the reclassification trigger. Those are the platform owner's own settings, so
// they sit behind admin where the rest of the tab is moderator-gated.
func (api *API) resolveAdminScope(r *http.Request) (*emoteScope, error) {
	user := ctxstore.GetUser(r.Context())
	if user == nil {
		return nil, fmt.Errorf("unauthorized")
	}

	isAdmin, _, err := api.db.HasPermission(r.Context(), user.TwitchUserID, db.PermissionAdmin)
	if err != nil {
		return nil, fmt.Errorf("failed to check admin permission: %w", err)
	}
	if !isAdmin {
		return nil, fmt.Errorf("platform defaults are admin-only")
	}

	return &emoteScope{Caller: user, CanGlobal: true, IsAdmin: true}, nil
}

func emoteErr(err error) template.HTML {
	return getHtml("error.html", &htmlErr{
		ErrorCode:    http.StatusInternalServerError,
		ErrorMessage: err.Error(),
	})
}

type emotesMenu struct {
	Panels          []PanelData
	CanGlobal       bool
	IsAdmin         bool
	PlatformClasses []emoteClassToggle
}

// emotesMenu lists the channels whose 7TV page the caller may open — their own,
// or every registered streamer for a platform moderator — with the global emote
// section above them.
func (api *API) emotesMenu(r *http.Request) template.HTML {
	user := ctxstore.GetUser(r.Context())
	if user == nil {
		return emoteErr(errors.New("unauthorized"))
	}

	canGlobal, err := api.canModerateEmotes(r, user)
	if err != nil {
		return emoteErr(err)
	}

	panels, err := api.emotePanels(r, user, canGlobal)
	if err != nil {
		return emoteErr(err)
	}

	out := &emotesMenu{Panels: panels, CanGlobal: canGlobal}

	isAdmin, _, err := api.db.HasPermission(r.Context(), user.TwitchUserID, db.PermissionAdmin)
	if err != nil {
		return emoteErr(fmt.Errorf("failed to check admin permission: %w", err))
	}
	if isAdmin && api.emotes != nil {
		blocked, err := api.emotes.GetPlatformSettings(r.Context())
		if err != nil {
			return emoteErr(fmt.Errorf("failed to get platform defaults: %w", err))
		}
		out.IsAdmin = true
		out.PlatformClasses = classToggles(blocked)
	}

	return getHtml("emotes_menu.html", out)
}

func (api *API) emotePanels(r *http.Request, user *db.User, canGlobal bool) ([]PanelData, error) {
	if canGlobal {
		streamers, err := api.db.GetUsersPermissions(r.Context(), db.PermissionStreamer, db.PermissionStatusGranted)
		if err != nil {
			return nil, fmt.Errorf("failed to get streamers: %w", err)
		}

		panels := make([]PanelData, 0, len(streamers))
		for _, streamer := range streamers {
			panels = append(panels, PanelData{
				TwitchLogin:  streamer.TwitchLogin,
				TwitchUserID: streamer.TwitchUserID,
			})
		}

		return panels, nil
	}

	isStreamer, _, err := api.db.HasPermission(r.Context(), user.TwitchUserID, db.PermissionStreamer)
	if err != nil {
		return nil, fmt.Errorf("failed to check streamer permission: %w", err)
	}
	if !isStreamer {
		return nil, nil
	}

	return []PanelData{{TwitchLogin: user.TwitchLogin, TwitchUserID: user.TwitchUserID}}, nil
}

// emoteClassLabels are the moderator-facing names for the classifier's classes,
// spelling out what each one actually covers so a toggle is not guesswork.
var emoteClassLabels = map[string]string{
	emoteservice.ClassCum:      "Cum innuendo (milk, cream, yogurt trope)",
	emoteservice.ClassSexual:   "Sexual (explicit or nudity)",
	emoteservice.ClassViolence: "Violence (gore, graphic, self-harm)",
	emoteservice.ClassHate:     "Hate (slurs, hate symbols, racist caricatures)",
	emoteservice.ClassFlashing: "Flashing (strobing, epilepsy hazard)",
	emoteservice.ClassDrugs:    "Drugs (use or paraphernalia)",
	emoteservice.ClassGambling: "Gambling (slots, casino, betting)",
	emoteservice.ClassFluids:   "Bodily fluids (urine, vomit, spit, sweat)",
}

type emoteClassToggle struct {
	Class   string
	Label   string
	Blocked bool
}

func classToggles(blocked []string) []emoteClassToggle {
	out := make([]emoteClassToggle, 0, len(emoteservice.ContentClasses))
	for _, class := range emoteservice.ContentClasses {
		out = append(out, emoteClassToggle{
			Class:   class,
			Label:   emoteClassLabels[class],
			Blocked: slices.Contains(blocked, class),
		})
	}

	return out
}

// blockedClassesFromForm keeps the checkboxes the browser sent that name a real
// class. The service filters again; this keeps junk out of the round trip.
func blockedClassesFromForm(r *http.Request) []string {
	return emoteservice.FilterContentClasses(r.Form["classes"])
}

type emotesPanel struct {
	TwitchLogin  string
	TwitchUserID int
	CanGlobal    bool
	Available    bool
	Inherited    bool
	Classes      []emoteClassToggle
}

func (api *API) emotesPanel(r *http.Request) template.HTML {
	scope, err := api.resolveEmoteScope(r)
	if err != nil {
		return emoteErr(err)
	}

	data := &emotesPanel{
		TwitchLogin:  scope.Target.TwitchLogin,
		TwitchUserID: scope.Target.TwitchUserID,
		CanGlobal:    scope.CanGlobal,
		Available:    api.emotes != nil,
	}

	if api.emotes != nil {
		settings, err := api.emotes.GetSettings(r.Context(), scope.StreamerID())
		if err != nil {
			return emoteErr(fmt.Errorf("failed to get emote settings: %w", err))
		}
		data.Inherited = settings.Inherited
		data.Classes = classToggles(settings.BlockedClasses)
	}

	return getHtml("emotes.html", data)
}

// emoteCard is one emote as the moderation UI renders it. ImageURL always points
// at our own endpoint; the template falls back to a placeholder on 404. A card
// with a ChannelPath carries that channel's controls, and only a platform
// moderator's card carries the global ones.
type emoteCard struct {
	Provider    string
	EmoteID     string
	Name        string
	BaseName    string
	Aliased     bool
	ImageURL    string
	Verdict     string
	Reason      string
	Score       string
	CanGlobal   bool
	ChannelPath string
}

func emoteImageURL(provider, emoteID string) string {
	return "/emote-image/" + emoteservice.NormalizeProvider(provider) + "/" + emoteID
}

// verdictReason turns a decision reason into something a moderator can read.
// A rule reason carries the rule id after a colon.
func verdictReason(reason string) string {
	if id, ok := strings.CutPrefix(reason, emoteservice.ReasonRule+":"); ok {
		return "blocked by rule " + id
	}
	if class, ok := strings.CutPrefix(reason, emoteservice.ReasonClass+":"); ok {
		return "class: " + class
	}

	switch reason {
	case emoteservice.ReasonStreamerBan:
		return "banned in this channel"
	case emoteservice.ReasonStreamerApprove:
		return "approved in this channel"
	case emoteservice.ReasonGlobalBan:
		return "banned globally"
	case emoteservice.ReasonGlobalApprove:
		return "approved globally"
	case emoteservice.ReasonDeleted:
		return "deleted on 7TV"
	case emoteservice.ReasonUnlisted:
		return "unlisted on 7TV"
	case emoteservice.ReasonAuto:
		return "auto-allowed"
	case emoteservice.ReasonUnclassified:
		return "not classified yet"
	default:
		return reason
	}
}

func (api *API) buildCards(r *http.Request, scope *emoteScope, cards []emoteCard) ([]emoteCard, error) {
	ids := make([]string, 0, len(cards))
	for _, c := range cards {
		ids = append(ids, c.EmoteID)
	}

	verdicts, err := api.emotes.Verdicts(r.Context(), scope.StreamerID(), ids)
	if err != nil {
		return nil, fmt.Errorf("failed to get verdicts: %w", err)
	}

	for i := range cards {
		cards[i].ImageURL = emoteImageURL(cards[i].Provider, cards[i].EmoteID)
		cards[i].CanGlobal = scope.CanGlobal
		cards[i].ChannelPath = scope.ChannelPath()
		cards[i].Aliased = cards[i].BaseName != "" && cards[i].BaseName != cards[i].Name

		d, ok := verdicts[cards[i].EmoteID]
		if !ok {
			cards[i].Verdict = string(emoteservice.VerdictUnknown)
			cards[i].Reason = verdictReason(emoteservice.ReasonUnclassified)

			continue
		}
		cards[i].Verdict = string(d.Verdict)
		cards[i].Reason = verdictReason(d.Reason)
	}

	return cards, nil
}

func setToCards(emotes []emoteservice.ChannelEmote) []emoteCard {
	cards := make([]emoteCard, 0, len(emotes))
	for _, e := range emotes {
		cards = append(cards, emoteCard{
			Provider: emoteservice.ProviderSevenTV,
			EmoteID:  e.EmoteID,
			Name:     e.Name,
			BaseName: e.BaseName,
		})
	}
	sort.SliceStable(cards, func(i, j int) bool {
		return strings.ToLower(cards[i].Name) < strings.ToLower(cards[j].Name)
	})

	return cards
}

type emoteChannelList struct {
	Linked   bool
	Username string
	Cards    []emoteCard
}

func (api *API) emotesChannel(r *http.Request) template.HTML {
	scope, err := api.resolveEmoteScope(r)
	if err != nil {
		return emoteErr(err)
	}
	if api.emotes == nil {
		return emoteErr(errors.New("emote service is not configured"))
	}

	set, err := api.emotes.ChannelSet(r.Context(), scope.Target.TwitchUserID)
	if err != nil {
		return emoteErr(fmt.Errorf("failed to get 7tv channel set: %w", err))
	}
	if !set.Linked {
		return getHtml("emote_channel.html", &emoteChannelList{})
	}

	cards, err := api.buildCards(r, scope, setToCards(set.Emotes))
	if err != nil {
		return emoteErr(err)
	}

	return getHtml("emote_channel.html", &emoteChannelList{
		Linked:   true,
		Username: set.Username,
		Cards:    cards,
	})
}

type emoteGlobalList struct {
	SetID string
	Cards []emoteCard
}

func (api *API) emotesGlobalSet(r *http.Request) template.HTML {
	scope, err := api.resolveGlobalScope(r)
	if err != nil {
		return emoteErr(err)
	}
	if api.emotes == nil {
		return emoteErr(errors.New("emote service is not configured"))
	}

	set, err := api.emotes.GlobalSet(r.Context())
	if err != nil {
		return emoteErr(fmt.Errorf("failed to get the 7tv global set: %w", err))
	}

	cards, err := api.buildCards(r, scope, setToCards(set.Emotes))
	if err != nil {
		return emoteErr(err)
	}

	return getHtml("emote_global.html", &emoteGlobalList{SetID: set.SetID, Cards: cards})
}

// emoteSearchResult is one query answered twice: by name, and — when the
// embedding server is up — by meaning. ContextualErr keeps the second failure
// visible instead of quietly serving half an answer.
type emoteSearchResult struct {
	Query         string
	Cards         []emoteCard
	Contextual    []emoteCard
	ContextualErr string
}

func (api *API) emotesSearch(r *http.Request) template.HTML {
	scope, err := api.resolveEmoteScope(r)
	if err != nil {
		return emoteErr(err)
	}

	return api.searchRegistry(r, scope)
}

func (api *API) emotesGlobalSearch(r *http.Request) template.HTML {
	scope, err := api.resolveGlobalScope(r)
	if err != nil {
		return emoteErr(err)
	}

	return api.searchRegistry(r, scope)
}

func (api *API) searchRegistry(r *http.Request, scope *emoteScope) template.HTML {
	if api.emotes == nil {
		return emoteErr(errors.New("emote service is not configured"))
	}

	query := strings.TrimSpace(r.URL.Query().Get("q"))

	emotes, err := api.emotes.SearchEmotes(r.Context(), query, emoteSearchLimit)
	if err != nil {
		return emoteErr(fmt.Errorf("failed to search emotes: %w", err))
	}

	out := &emoteSearchResult{Query: query}

	named := make(map[string]struct{}, len(emotes))
	cards := make([]emoteCard, 0, len(emotes))
	for _, e := range emotes {
		named[e.Provider+"/"+e.ID] = struct{}{}
		cards = append(cards, emoteCard{
			Provider: e.Provider,
			EmoteID:  e.ID,
			Name:     e.Name,
		})
	}
	nameCount := len(cards)

	if query != "" {
		results, err := api.emotes.SemanticSearch(r.Context(), query, emoteSemanticLimit)
		if err != nil {
			api.logger.Error("contextual emote search failed", "err", err, "q", query)
			out.ContextualErr = err.Error()
		}

		for _, c := range results {
			if _, dup := named[c.Provider+"/"+c.EmoteID]; dup {
				continue
			}
			cards = append(cards, emoteCard{
				Provider: c.Provider,
				EmoteID:  c.EmoteID,
				Name:     c.Name,
				Score:    strconv.FormatFloat(c.Score, 'f', 3, 64),
			})
		}
	}

	// Both sections are decided in one verdict round trip.
	cards, err = api.buildCards(r, scope, cards)
	if err != nil {
		return emoteErr(err)
	}

	out.Cards, out.Contextual = cards[:nameCount], cards[nameCount:]

	return getHtml("emote_search.html", out)
}

// emoteQueueStatus is the classification backlog. It is service-wide, not
// per-channel, which is why it lives on the tab's landing page. Total is
// computed here: /v1/queue answers with counts per status and nothing else.
type emoteQueueStatus struct {
	Statuses []emoteQueueEntry
	Total    int
	// Outdated counts rows judged under an older taxonomy; they hold a verdict
	// that no longer means what the current classes mean.
	Outdated int
	Version  int
}

type emoteQueueEntry struct {
	Status string
	Count  int
}

func (api *API) emotesQueue(r *http.Request) template.HTML {
	if api.emotes == nil {
		return emoteErr(errors.New("emote service is not configured"))
	}

	stats, err := api.emotes.QueueStats(r.Context())
	if err != nil {
		return emoteErr(fmt.Errorf("failed to get queue stats: %w", err))
	}

	out := &emoteQueueStatus{
		Statuses: make([]emoteQueueEntry, 0, len(stats.Statuses)),
		Outdated: stats.Outdated,
		Version:  stats.ClassifierVersion,
	}
	for status, count := range stats.Statuses {
		out.Statuses = append(out.Statuses, emoteQueueEntry{Status: status, Count: count})
		out.Total += count
	}
	sort.Slice(out.Statuses, func(i, j int) bool { return out.Statuses[i].Status < out.Statuses[j].Status })

	return getHtml("emote_queue.html", out)
}

// prepareAction finishes a mutating request: it reports the authorization
// failure to the browser and parses the form.
func (api *API) prepareAction(w http.ResponseWriter, r *http.Request, scope *emoteScope, err error) (*emoteScope, bool) {
	if err != nil {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(err.Error()))

		return nil, false
	}
	if api.emotes == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("emote service is not configured"))

		return nil, false
	}
	if err := r.ParseForm(); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("failed to parse form: " + err.Error()))

		return nil, false
	}

	return scope, true
}

// channelAction authorizes a mutating request scoped to one channel.
func (api *API) channelAction(w http.ResponseWriter, r *http.Request) (*emoteScope, bool) {
	scope, err := api.resolveEmoteScope(r)

	return api.prepareAction(w, r, scope, err)
}

// globalAction authorizes a mutating request with no channel of its own.
func (api *API) globalAction(w http.ResponseWriter, r *http.Request) (*emoteScope, bool) {
	scope, err := api.resolveGlobalScope(r)

	return api.prepareAction(w, r, scope, err)
}

func (api *API) emotesEnqueue(w http.ResponseWriter, r *http.Request) {
	scope, ok := api.channelAction(w, r)
	if !ok {
		return
	}

	emoteID, err := parseEmoteRef(r.FormValue("emote_ref"))
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(err.Error()))

		return
	}

	if _, err := api.emotes.Enqueue(r.Context(), []string{emoteID}, emotePriorityManual); err != nil {
		api.logger.Error("failed to enqueue emote", "err", err, "emote_id", emoteID, "actor", scope.Caller.TwitchLogin)
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("failed to enqueue emote"))

		return
	}

	api.logger.Info("emote queued for classification",
		"emote_id", emoteID, "actor", scope.Caller.TwitchLogin, "actor_id", scope.Caller.ID)

	_, _ = w.Write([]byte("queued " + emoteID + " for classification"))
}

// emotesEnqueueChannelSet classifies the whole active set of the channel whose
// page this is.
func (api *API) emotesEnqueueChannelSet(w http.ResponseWriter, r *http.Request) {
	scope, ok := api.channelAction(w, r)
	if !ok {
		return
	}

	api.enqueueSet(w, r, scope, emoteSetRef{}, scope.Target.TwitchUserID)
}

// emotesEnqueueSet classifies any set a platform moderator points it at.
func (api *API) emotesEnqueueSet(w http.ResponseWriter, r *http.Request) {
	scope, ok := api.globalAction(w, r)
	if !ok {
		return
	}

	ref, err := parseEmoteSetRef(r.FormValue("set_ref"))
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(err.Error()))

		return
	}

	var twitchUserID int
	if ref.TwitchLogin != "" {
		twitchUserID, err = api.resolveTwitchUserID(r, scope.Caller, ref.TwitchLogin)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(err.Error()))

			return
		}
	}

	api.enqueueSet(w, r, scope, ref, twitchUserID)
}

// enqueueSet is a deliberate burst of GPU work, so the actor is logged.
func (api *API) enqueueSet(w http.ResponseWriter, r *http.Request, scope *emoteScope, ref emoteSetRef, twitchUserID int) {
	result, err := api.emotes.EnqueueSet(r.Context(), ref.SetID, twitchUserID, emotePrioritySet)
	if err != nil {
		api.logger.Error("failed to enqueue emote set", "err", err,
			"set_id", ref.SetID, "twitch_user_id", twitchUserID, "actor", scope.Caller.TwitchLogin)
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("failed to enqueue emote set: " + err.Error()))

		return
	}

	api.logger.Info("emote set queued for classification",
		"set_id", result.SetID, "emotes", result.Emotes, "queued", result.Queued,
		"actor", scope.Caller.TwitchLogin, "actor_id", scope.Caller.ID)

	if result.Emotes == 0 {
		_, _ = w.Write([]byte("that set is empty, nothing queued"))

		return
	}

	_, _ = fmt.Fprintf(w, "queued %d of %d emotes from set %s", result.Queued, result.Emotes, result.SetID)
}

// resolveTwitchUserID turns a channel login into its Twitch id, using the
// caller's own Twitch credentials the way the control panel grant does.
func (api *API) resolveTwitchUserID(r *http.Request, caller *db.User, login string) (int, error) {
	twitchAPI, err := api.helixForUser(r.Context(), caller)
	if err != nil {
		return 0, fmt.Errorf("twitch client error: %w", err)
	}

	resp, err := twitchAPI.GetUsers(&helix.UsersParams{Logins: []string{login}})
	if err != nil {
		return 0, fmt.Errorf("twitch lookup failed: %w", err)
	}
	if resp == nil || len(resp.Data.Users) == 0 {
		return 0, fmt.Errorf("twitch channel %q not found", login)
	}

	id, err := strconv.Atoi(resp.Data.Users[0].ID)
	if err != nil {
		return 0, fmt.Errorf("twitch returned a non-numeric id for %q: %w", login, err)
	}

	return id, nil
}

// emoteOverrideScope maps the form's scope onto a streamer id. A channel page
// can also post global overrides, so the moderator role is re-checked here and
// not only on the route.
func emoteOverrideScope(scope *emoteScope, raw string) (string, error) {
	if scope.Target == nil {
		return emoteservice.GlobalScope, nil
	}
	if raw == "global" {
		if !scope.CanGlobal {
			return "", errors.New("global 7TV emotes are restricted to platform moderators")
		}

		return emoteservice.GlobalScope, nil
	}

	return scope.StreamerID(), nil
}

func (api *API) emotesOverride(w http.ResponseWriter, r *http.Request) {
	scope, ok := api.channelAction(w, r)
	if !ok {
		return
	}

	api.applyOverride(w, r, scope)
}

func (api *API) emotesGlobalOverride(w http.ResponseWriter, r *http.Request) {
	scope, ok := api.globalAction(w, r)
	if !ok {
		return
	}

	api.applyOverride(w, r, scope)
}

func (api *API) applyOverride(w http.ResponseWriter, r *http.Request, scope *emoteScope) {
	emoteID := r.FormValue("emote_id")
	if !emoteIDPattern.MatchString(emoteID) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("invalid emote id"))

		return
	}

	streamerID, err := emoteOverrideScope(scope, r.FormValue("scope"))
	if err != nil {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(err.Error()))

		return
	}

	override := emoteservice.Override{
		StreamerID: streamerID,
		Provider:   emoteservice.ProviderSevenTV,
		EmoteID:    emoteID,
		Author:     scope.Caller.TwitchLogin,
	}

	switch r.FormValue("action") {
	case "approve":
		override.Verdict = emoteservice.VerdictAllow
		err = api.emotes.AddOverride(r.Context(), override)
	case "ban":
		override.Verdict = emoteservice.VerdictBlock
		err = api.emotes.AddOverride(r.Context(), override)
	case "clear":
		err = api.clearOverride(r, override)
	default:
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("unknown action"))

		return
	}
	if err != nil {
		api.logger.Error("failed to write emote override", "err", err, "emote_id", emoteID,
			"streamer_id", streamerID, "actor", scope.Caller.TwitchLogin)
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("failed to update emote: " + err.Error()))

		return
	}

	card := emoteCard{
		Provider: emoteservice.ProviderSevenTV,
		EmoteID:  emoteID,
		Name:     r.FormValue("name"),
		BaseName: r.FormValue("base_name"),
		Score:    r.FormValue("score"),
	}
	cards, err := api.buildCards(r, scope, []emoteCard{card})
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(err.Error()))

		return
	}

	_, _ = w.Write([]byte(getHtml("emote_card.html", cards[0])))
}

// clearOverride drops both verdicts for one emote in one scope. A verdict that
// was never set answers 404, which is the intended end state either way.
func (api *API) clearOverride(r *http.Request, o emoteservice.Override) error {
	for _, verdict := range []emoteservice.Verdict{emoteservice.VerdictAllow, emoteservice.VerdictBlock} {
		o.Verdict = verdict

		err := api.emotes.DeleteOverride(r.Context(), o)

		var httpErr *emoteHTTPError
		if errors.As(err, &httpErr) && httpErr.Status == http.StatusNotFound {
			continue
		}
		if err != nil {
			return err
		}
	}

	return nil
}

// emotesSettings materializes the channel's own blocked-class set; until this
// runs the channel is inheriting the platform default.
func (api *API) emotesSettings(w http.ResponseWriter, r *http.Request) {
	scope, ok := api.channelAction(w, r)
	if !ok {
		return
	}

	settings := emoteservice.Settings{
		StreamerID:     scope.StreamerID(),
		BlockedClasses: blockedClassesFromForm(r),
	}

	if err := api.emotes.PutSettings(r.Context(), settings); err != nil {
		api.logger.Error("failed to update emote settings", "err", err, "streamer", scope.Target.TwitchLogin)
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("failed to update settings"))

		return
	}

	_, _ = w.Write([]byte("saved"))
}

func (api *API) emotesSettingsReset(w http.ResponseWriter, r *http.Request) {
	scope, ok := api.channelAction(w, r)
	if !ok {
		return
	}

	settings, err := api.emotes.ResetSettings(r.Context(), scope.StreamerID())
	if err != nil {
		api.logger.Error("failed to reset emote settings", "err", err, "streamer", scope.Target.TwitchLogin)
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("failed to reset settings"))

		return
	}

	_, _ = w.Write([]byte(getHtml("emote_settings.html", &emoteSettingsForm{
		TwitchUserID: scope.Target.TwitchUserID,
		Inherited:    settings.Inherited,
		Classes:      classToggles(settings.BlockedClasses),
	})))
}

// emoteSettingsForm re-renders the toggles after a reset, so the inherited
// values appear without a page reload.
type emoteSettingsForm struct {
	TwitchUserID int
	Inherited    bool
	Classes      []emoteClassToggle
}

// emotesPlatformSettings edits the default every channel inherits. It is the
// platform owner's own knob, hence admin-only where the rest of the tab is
// moderator-gated.
func (api *API) emotesPlatformSettings(w http.ResponseWriter, r *http.Request) {
	scope, err := api.resolveAdminScope(r)
	scope, ok := api.prepareAction(w, r, scope, err)
	if !ok {
		return
	}

	blocked := blockedClassesFromForm(r)

	if err := api.emotes.PutPlatformSettings(r.Context(), blocked); err != nil {
		api.logger.Error("failed to update platform emote settings", "err", err, "actor", scope.Caller.TwitchLogin)
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("failed to update platform defaults"))

		return
	}

	api.logger.Info("platform emote defaults updated",
		"blocked", blocked, "actor", scope.Caller.TwitchLogin, "actor_id", scope.Caller.ID)

	_, _ = w.Write([]byte("saved"))
}

func (api *API) emotesReclassify(w http.ResponseWriter, r *http.Request) {
	scope, err := api.resolveAdminScope(r)
	scope, ok := api.prepareAction(w, r, scope, err)
	if !ok {
		return
	}

	all := r.FormValue("all") == "on"

	enqueued, err := api.emotes.Reclassify(r.Context(), all)
	if err != nil {
		api.logger.Error("failed to queue reclassification", "err", err, "actor", scope.Caller.TwitchLogin)
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("failed to queue reclassification: " + err.Error()))

		return
	}

	api.logger.Info("reclassification queued",
		"enqueued", enqueued, "all", all, "actor", scope.Caller.TwitchLogin, "actor_id", scope.Caller.ID)

	_, _ = fmt.Fprintf(w, "queued %d emotes for reclassification", enqueued)
}

type emoteRulesList struct {
	TwitchUserID int
	Rules        []emoteservice.Rule
}

func (api *API) emotesRules(r *http.Request) template.HTML {
	scope, err := api.resolveEmoteScope(r)
	if err != nil {
		return emoteErr(err)
	}
	if api.emotes == nil {
		return emoteErr(errors.New("emote service is not configured"))
	}

	rules, err := api.emotes.ListRules(r.Context(), scope.StreamerID())
	if err != nil {
		return emoteErr(fmt.Errorf("failed to list rules: %w", err))
	}

	return getHtml("emote_rules.html", &emoteRulesList{
		TwitchUserID: scope.Target.TwitchUserID,
		Rules:        rules,
	})
}

// emoteRulePreview is the confirm-preview: the rule as stored plus the emotes it
// proposes to block. Nothing is enforced until the streamer confirms a subset.
type emoteRulePreview struct {
	TwitchUserID int
	Rule         *emoteservice.Rule
	Candidates   []emoteRuleCandidate
	PreviewError string
}

type emoteRuleCandidate struct {
	Provider    string
	EmoteID     string
	Name        string
	Description string
	ImageURL    string
	Sources     string
	Score       string
}

func renderRulePreview(twitchUserID int, preview *rulePreview) template.HTML {
	out := &emoteRulePreview{
		TwitchUserID: twitchUserID,
		Rule:         preview.Rule,
		PreviewError: preview.PreviewError,
		Candidates:   make([]emoteRuleCandidate, 0, len(preview.Candidates)),
	}

	for _, c := range preview.Candidates {
		out.Candidates = append(out.Candidates, emoteRuleCandidate{
			Provider:    c.Provider,
			EmoteID:     c.EmoteID,
			Name:        c.Name,
			Description: c.Description,
			ImageURL:    emoteImageURL(c.Provider, c.EmoteID),
			Sources:     strings.Join(c.Sources, ", "),
			Score:       strconv.FormatFloat(c.Score, 'f', 3, 64),
		})
	}

	return getHtml("emote_rule_preview.html", out)
}

func (api *API) emotesRuleCreate(w http.ResponseWriter, r *http.Request) {
	scope, ok := api.channelAction(w, r)
	if !ok {
		return
	}

	ruleText := strings.TrimSpace(r.FormValue("rule_text"))
	if ruleText == "" {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("rule text is required"))

		return
	}

	var threshold *float64
	if raw := strings.TrimSpace(r.FormValue("threshold")); raw != "" {
		parsed, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte("threshold must be a number"))

			return
		}
		threshold = &parsed
	}

	preview, err := api.emotes.CreateRule(r.Context(), scope.StreamerID(), ruleText, threshold)
	if err != nil {
		api.logger.Error("failed to create emote rule", "err", err, "streamer", scope.Target.TwitchLogin)
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("failed to create rule: " + err.Error()))

		return
	}

	_, _ = w.Write([]byte(renderRulePreview(scope.Target.TwitchUserID, preview)))
}

func (api *API) emotesRulePreview(w http.ResponseWriter, r *http.Request) {
	scope, ok := api.channelAction(w, r)
	if !ok {
		return
	}

	ruleID, ok := api.ruleInScope(w, r, scope)
	if !ok {
		return
	}

	preview, err := api.emotes.PreviewRule(r.Context(), ruleID)
	if err != nil {
		api.logger.Error("failed to preview emote rule", "err", err, "rule_id", ruleID)
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("failed to preview rule: " + err.Error()))

		return
	}

	_, _ = w.Write([]byte(renderRulePreview(scope.Target.TwitchUserID, preview)))
}

func (api *API) emotesRuleConfirm(w http.ResponseWriter, r *http.Request) {
	scope, ok := api.channelAction(w, r)
	if !ok {
		return
	}

	ruleID, ok := api.ruleInScope(w, r, scope)
	if !ok {
		return
	}

	emoteIDs := make([]string, 0, len(r.Form["emote_ids"]))
	for _, id := range r.Form["emote_ids"] {
		if emoteIDPattern.MatchString(id) {
			emoteIDs = append(emoteIDs, id)
		}
	}

	blocked, err := api.emotes.ConfirmRule(r.Context(), ruleID, emoteIDs)
	if err != nil {
		api.logger.Error("failed to confirm emote rule", "err", err, "rule_id", ruleID)
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("failed to confirm rule: " + err.Error()))

		return
	}

	_, _ = fmt.Fprintf(w, "rule now blocks %d emotes", blocked)
}

func (api *API) emotesRuleDelete(w http.ResponseWriter, r *http.Request) {
	scope, ok := api.channelAction(w, r)
	if !ok {
		return
	}

	ruleID, ok := api.ruleInScope(w, r, scope)
	if !ok {
		return
	}

	if err := api.emotes.DeleteRule(r.Context(), ruleID); err != nil {
		api.logger.Error("failed to delete emote rule", "err", err, "rule_id", ruleID)
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("failed to delete rule: " + err.Error()))

		return
	}

	_, _ = w.Write([]byte("deleted"))
}

// ruleInScope guards the rule routes, which the service addresses by bare id:
// without this a moderator could preview, confirm or delete another channel's
// rule by guessing its id.
func (api *API) ruleInScope(w http.ResponseWriter, r *http.Request, scope *emoteScope) (string, bool) {
	ruleID := chi.URLParam(r, "rule_id")

	rules, err := api.emotes.ListRules(r.Context(), scope.StreamerID())
	if err != nil {
		api.logger.Error("failed to list rules", "err", err, "streamer", scope.Target.TwitchLogin)
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("failed to list rules"))

		return "", false
	}

	for _, rule := range rules {
		if rule.ID == ruleID {
			return ruleID, true
		}
	}

	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte("rule not found"))

	return "", false
}
