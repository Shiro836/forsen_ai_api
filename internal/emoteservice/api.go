package emoteservice

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"app/pkg/seventv"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// AuthHeader carries the shared secret. Per-user auth arrives with the
// moderation UI; until then every caller is a trusted service.
const AuthHeader = "X-Emote-Token"

func (s *Service) Router() http.Handler {
	r := chi.NewRouter()

	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	r.Route("/v1", func(r chi.Router) {
		r.Use(s.authMiddleware)

		r.Post("/verdicts", s.handleVerdicts)

		r.Post("/enqueue", s.handleEnqueue)
		r.Post("/enqueue-set", s.handleEnqueueSet)
		r.Post("/seed", s.handleSeed)
		r.Get("/queue", s.handleQueueStats)

		r.Get("/emotes", s.handleSearchEmotes)
		r.Get("/search", s.handleSemanticSearch)
		r.Get("/emotes/{emoteID}", s.handleGetEmote)

		r.Get("/channel-set", s.handleChannelSet)
		r.Get("/global-set", s.handleGlobalSet)

		r.Get("/overrides", s.handleListOverrides)
		r.Post("/overrides", s.handleAddOverride)
		r.Delete("/overrides", s.handleDeleteOverride)

		r.Post("/reclassify", s.handleReclassify)
		r.Post("/reflash", s.handleReflash)

		r.Get("/settings", s.handleGetSettings)
		r.Put("/settings", s.handlePutSettings)
		r.Delete("/settings", s.handleResetSettings)

		r.Get("/platform-settings", s.handleGetPlatformSettings)
		r.Put("/platform-settings", s.handlePutPlatformSettings)

		r.Get("/rules", s.handleListRules)
		r.Post("/rules", s.handleCreateRule)
		r.Delete("/rules/{ruleID}", s.handleDeleteRule)
		r.Post("/rules/{ruleID}/preview", s.handlePreviewRule)
		r.Post("/rules/{ruleID}/confirm", s.handleConfirmRule)
	})

	return r
}

func (s *Service) authMiddleware(next http.Handler) http.Handler {
	secret := []byte(s.cfg.SharedSecret)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := []byte(r.Header.Get(AuthHeader))
		if subtle.ConstantTimeCompare(got, secret) != 1 {
			writeError(w, http.StatusUnauthorized, "invalid token")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// writeStoreError maps a store failure onto a status code and logs the ones
// that are the service's own fault.
func (s *Service) writeStoreError(w http.ResponseWriter, op string, err error) {
	if errors.Is(err, ErrNotFound) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	s.logger.Error("request failed", "op", op, "err", err)
	writeError(w, http.StatusInternalServerError, "internal error")
}

// parseScope normalizes a streamer id. Empty means the global scope; anything
// else must be a users.id uuid, since that is the column type.
func parseScope(w http.ResponseWriter, raw string) (string, bool) {
	if raw == "" {
		return GlobalScope, true
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		writeError(w, http.StatusBadRequest, "streamer_id must be a uuid")
		return "", false
	}
	return id.String(), true
}

// parseStreamer is parseScope for routes that only make sense for a real channel.
func parseStreamer(w http.ResponseWriter, raw string) (string, bool) {
	id, ok := parseScope(w, raw)
	if !ok {
		return "", false
	}
	if id == GlobalScope {
		writeError(w, http.StatusBadRequest, "streamer_id is required")
		return "", false
	}
	return id, true
}

func decodeBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json body")
		return false
	}
	return true
}

// Emote-addressing requests carry one provider for the whole batch; it defaults
// to the only provider implemented.
type verdictsRequest struct {
	StreamerID string   `json:"streamer_id"`
	Provider   string   `json:"provider"`
	EmoteIDs   []string `json:"emote_ids"`
}

func (s *Service) handleVerdicts(w http.ResponseWriter, r *http.Request) {
	var req verdictsRequest
	if !decodeBody(w, r, &req) {
		return
	}
	streamerID, ok := parseScope(w, req.StreamerID)
	if !ok {
		return
	}
	if len(req.EmoteIDs) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"verdicts": map[string]Decision{}})
		return
	}

	ctx := r.Context()
	settings, err := s.store.GetSettings(ctx, streamerID)
	if err != nil {
		s.writeStoreError(w, "get settings", err)
		return
	}
	facts, err := s.store.LoadFacts(ctx, streamerID, req.Provider, req.EmoteIDs)
	if err != nil {
		s.writeStoreError(w, "load facts", err)
		return
	}

	out := make(map[string]Decision, len(req.EmoteIDs))
	for _, id := range req.EmoteIDs {
		d := Decide(facts[id], settings)
		d.EmoteID = id
		out[id] = d
	}
	writeJSON(w, http.StatusOK, map[string]any{"verdicts": out})
}

type enqueueRequest struct {
	Provider string   `json:"provider"`
	EmoteIDs []string `json:"emote_ids"`
	Priority int      `json:"priority"`
}

// handleEnqueue names emotes explicitly, which is always a person asking for
// these ones to be judged, so it forces: a caller who hands over an id list and
// gets back a clean drain and an unchanged row has been told nothing.
func (s *Service) handleEnqueue(w http.ResponseWriter, r *http.Request) {
	var req enqueueRequest
	if !decodeBody(w, r, &req) {
		return
	}
	n, err := s.store.Enqueue(r.Context(), req.Provider, req.EmoteIDs, req.Priority, true)
	if err != nil {
		s.writeStoreError(w, "enqueue", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"queued": n})
}

type seedRequest struct {
	Count    int          `json:"count"`
	Queries  []SeedSource `json:"queries"`
	Priority int          `json:"priority"`
}

// handleSeed runs the popularity crawls inline — 8 requests and about 10 seconds
// per 2000 — and answers with what actually landed in the queue. A body naming
// neither a count nor queries runs the configured sources.
func (s *Service) handleSeed(w http.ResponseWriter, r *http.Request) {
	var req seedRequest
	if !decodeBody(w, r, &req) {
		return
	}

	var sources []SeedSource
	if req.Count > 0 {
		sources = append(sources, SeedSource{Count: req.Count})
	}
	sources = append(sources, req.Queries...)

	queued, err := s.worker.Seed(r.Context(), sources, req.Priority)
	if errors.Is(err, ErrSeedingUnavailable) {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	if err != nil {
		s.logger.Error("seeding failed", "count", req.Count, "err", err)
		writeError(w, http.StatusBadGateway, "seeding failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"queued": queued})
}

// QueueStats is the classification backlog: the queue's own statuses, plus how
// many already-classified rows are stale against the current taxonomy.
type QueueStats struct {
	Statuses           map[string]int `json:"statuses"`
	ClassifierVersions map[int]int    `json:"classifier_versions"`
	ClassifierVersion  int            `json:"classifier_version"`
	Outdated           int            `json:"outdated"`
}

func (s *Service) handleQueueStats(w http.ResponseWriter, r *http.Request) {
	statuses, err := s.store.QueueStats(r.Context())
	if err != nil {
		s.writeStoreError(w, "queue stats", err)
		return
	}

	versions, err := s.store.ClassifierVersionStats(r.Context())
	if err != nil {
		s.writeStoreError(w, "classifier version stats", err)
		return
	}

	out := QueueStats{Statuses: statuses, ClassifierVersions: versions, ClassifierVersion: ClassifierVersion}
	for version, n := range versions {
		if version < ClassifierVersion {
			out.Outdated += n
		}
	}

	writeJSON(w, http.StatusOK, out)
}

func (s *Service) handleSearchEmotes(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	emotes, err := s.store.SearchEmotes(r.Context(), r.URL.Query().Get("q"), limit)
	if err != nil {
		s.writeStoreError(w, "search emotes", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"emotes": emotes})
}

// Search page bounds. The caller renders every hit as an image, and every hit
// costs a cosine over the whole cache.
const (
	defaultSearchLimit = 50
	maxSearchLimit     = 200
)

// handleSemanticSearch ranks the classified registry against free text, using
// the same image/description embeddings and union-by-best-score merge that rule
// previews run on.
func (s *Service) handleSemanticSearch(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if query == "" {
		writeError(w, http.StatusBadRequest, "q is required")
		return
	}

	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = defaultSearchLimit
	}
	if limit > maxSearchLimit {
		limit = maxSearchLimit
	}

	results, err := s.matcher.Search(r.Context(), query, limit)
	if errors.Is(err, ErrEmbedderUnavailable) {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	if err != nil {
		s.logger.Error("semantic search failed", "q", query, "err", err)
		writeError(w, http.StatusBadGateway, "semantic search failed: "+err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"results": results})
}

func (s *Service) handleGetEmote(w http.ResponseWriter, r *http.Request) {
	emote, err := s.store.GetEmote(r.Context(), r.URL.Query().Get("provider"), chi.URLParam(r, "emoteID"))
	if err != nil {
		s.writeStoreError(w, "get emote", err)
		return
	}
	writeJSON(w, http.StatusOK, emote)
}

// ChannelEmote is one entry of a channel's active 7TV set. Name is the channel's
// alias, EmoteID is what the moderation lists key on.
type ChannelEmote struct {
	EmoteID   string `json:"emote_id"`
	Name      string `json:"name"`
	BaseName  string `json:"base_name"`
	ZeroWidth bool   `json:"zero_width"`
}

// ChannelSet is a channel's 7TV set. Linked is false when the channel has never
// linked a 7TV account, which is an ordinary outcome rather than an error.
type ChannelSet struct {
	Linked     bool           `json:"linked"`
	TwitchID   string         `json:"twitch_id"`
	Username   string         `json:"username,omitempty"`
	EmoteSetID string         `json:"emote_set_id,omitempty"`
	Emotes     []ChannelEmote `json:"emotes"`
}

func (s *Service) handleChannelSet(w http.ResponseWriter, r *http.Request) {
	twitchID := r.URL.Query().Get("twitch_id")
	if twitchID == "" {
		writeError(w, http.StatusBadRequest, "twitch_id is required")
		return
	}

	user, err := s.channels.GetTwitchUser(r.Context(), twitchID)
	if errors.Is(err, seventv.ErrNoAccount) {
		writeJSON(w, http.StatusOK, ChannelSet{TwitchID: twitchID, Emotes: []ChannelEmote{}})
		return
	}
	if err != nil {
		s.logger.Error("failed to fetch channel emote set", "twitch_id", twitchID, "err", err)
		writeError(w, http.StatusBadGateway, "failed to fetch channel emote set")
		return
	}

	writeJSON(w, http.StatusOK, ChannelSet{
		Linked:     true,
		TwitchID:   twitchID,
		Username:   user.Username,
		EmoteSetID: user.EmoteSetID,
		Emotes:     toChannelEmotes(user.Set),
	})
}

// EmoteSetResponse is a bare emote set, used for the 7TV global set and for the
// set an enqueue-set request resolved.
type EmoteSetResponse struct {
	SetID  string         `json:"set_id"`
	Name   string         `json:"name,omitempty"`
	Emotes []ChannelEmote `json:"emotes"`
}

func toChannelEmotes(set seventv.EmoteSet) []ChannelEmote {
	out := make([]ChannelEmote, 0, len(set.Emotes))
	for _, e := range set.Emotes {
		out = append(out, ChannelEmote{
			EmoteID:   e.EmoteID,
			Name:      e.Name,
			BaseName:  e.BaseName,
			ZeroWidth: e.ZeroWidth,
		})
	}
	return out
}

func (s *Service) handleGlobalSet(w http.ResponseWriter, r *http.Request) {
	set, err := s.channels.GetEmoteSet(r.Context(), seventv.GlobalEmoteSet)
	if err != nil {
		s.logger.Error("failed to fetch global emote set", "err", err)
		writeError(w, http.StatusBadGateway, "failed to fetch global emote set")
		return
	}

	writeJSON(w, http.StatusOK, EmoteSetResponse{SetID: set.ID, Name: set.Name, Emotes: toChannelEmotes(*set)})
}

// enqueueSetRequest names a set either directly or by the Twitch channel whose
// active set it is.
type enqueueSetRequest struct {
	SetID    string `json:"set_id"`
	TwitchID string `json:"twitch_id"`
	Priority int    `json:"priority"`
}

type enqueueSetResponse struct {
	SetID  string `json:"set_id"`
	Emotes int    `json:"emotes"`
	Queued int    `json:"queued"`
}

// handleEnqueueSet classifies a whole emote set on demand. It is deliberate
// compute spend by a moderator, so it enqueues every entry rather than
// filtering: what is already classified is skipped by the worker anyway.
func (s *Service) handleEnqueueSet(w http.ResponseWriter, r *http.Request) {
	var req enqueueSetRequest
	if !decodeBody(w, r, &req) {
		return
	}

	var (
		set *seventv.EmoteSet
		err error
	)
	switch {
	case req.SetID != "":
		set, err = s.channels.GetEmoteSet(r.Context(), req.SetID)
	case req.TwitchID != "":
		var user *seventv.TwitchUser
		user, err = s.channels.GetTwitchUser(r.Context(), req.TwitchID)
		if errors.Is(err, seventv.ErrNoAccount) {
			writeJSON(w, http.StatusOK, enqueueSetResponse{})
			return
		}
		if user != nil {
			set = &user.Set
		}
	default:
		writeError(w, http.StatusBadRequest, "set_id or twitch_id is required")
		return
	}
	if err != nil {
		s.logger.Error("failed to fetch emote set", "set_id", req.SetID, "twitch_id", req.TwitchID, "err", err)
		writeError(w, http.StatusBadGateway, "failed to fetch emote set")
		return
	}

	seen := make(map[string]struct{}, len(set.Emotes))
	ids := make([]string, 0, len(set.Emotes))
	for _, e := range set.Emotes {
		if _, dup := seen[e.EmoteID]; dup {
			continue
		}
		seen[e.EmoteID] = struct{}{}
		ids = append(ids, e.EmoteID)
	}

	// A whole set is bulk catch-up, not a re-judgement request: forcing here would
	// spend a vision call on every already-classified entry of a thousand-emote
	// set. /v1/reclassify is the way to ask for those again.
	queued, err := s.store.Enqueue(r.Context(), ProviderSevenTV, ids, req.Priority, false)
	if err != nil {
		s.writeStoreError(w, "enqueue set", err)
		return
	}

	s.logger.Info("emote set enqueued", "set_id", set.ID, "emotes", len(ids), "queued", queued)
	writeJSON(w, http.StatusOK, enqueueSetResponse{SetID: set.ID, Emotes: len(ids), Queued: queued})
}

func (s *Service) handleListOverrides(w http.ResponseWriter, r *http.Request) {
	streamerID, ok := parseScope(w, r.URL.Query().Get("streamer_id"))
	if !ok {
		return
	}
	overrides, err := s.store.ListOverrides(r.Context(), streamerID)
	if err != nil {
		s.writeStoreError(w, "list overrides", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"overrides": overrides})
}

func (s *Service) handleAddOverride(w http.ResponseWriter, r *http.Request) {
	var o Override
	if !decodeBody(w, r, &o) {
		return
	}
	if o.EmoteID == "" {
		writeError(w, http.StatusBadRequest, "emote_id is required")
		return
	}
	if o.Verdict != VerdictAllow && o.Verdict != VerdictBlock {
		writeError(w, http.StatusBadRequest, "verdict must be allow or block")
		return
	}
	streamerID, ok := parseScope(w, o.StreamerID)
	if !ok {
		return
	}
	o.StreamerID = streamerID
	if err := s.store.AddOverride(r.Context(), o); err != nil {
		s.writeStoreError(w, "add override", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Service) handleDeleteOverride(w http.ResponseWriter, r *http.Request) {
	var o Override
	if !decodeBody(w, r, &o) {
		return
	}
	streamerID, ok := parseScope(w, o.StreamerID)
	if !ok {
		return
	}
	if err := s.store.DeleteOverride(r.Context(), streamerID, o.Provider, o.EmoteID, o.Verdict); err != nil {
		s.writeStoreError(w, "delete override", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Service) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	streamerID, ok := parseStreamer(w, r.URL.Query().Get("streamer_id"))
	if !ok {
		return
	}
	settings, err := s.store.GetSettings(r.Context(), streamerID)
	if err != nil {
		s.writeStoreError(w, "get settings", err)
		return
	}
	writeJSON(w, http.StatusOK, settings)
}

// handlePutSettings materializes a channel's own choice. Unknown class names are
// dropped rather than rejected, so a stale client cannot wedge the form.
func (s *Service) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	var settings Settings
	if !decodeBody(w, r, &settings) {
		return
	}
	streamerID, ok := parseStreamer(w, settings.StreamerID)
	if !ok {
		return
	}
	settings.StreamerID = streamerID
	settings.BlockedClasses = FilterContentClasses(settings.BlockedClasses)
	settings.Inherited = false

	if err := s.store.UpsertSettings(r.Context(), settings); err != nil {
		s.writeStoreError(w, "upsert settings", err)
		return
	}
	writeJSON(w, http.StatusOK, settings)
}

func (s *Service) handleResetSettings(w http.ResponseWriter, r *http.Request) {
	streamerID, ok := parseStreamer(w, r.URL.Query().Get("streamer_id"))
	if !ok {
		return
	}
	if err := s.store.ResetSettings(r.Context(), streamerID); err != nil {
		s.writeStoreError(w, "reset settings", err)
		return
	}
	settings, err := s.store.GetSettings(r.Context(), streamerID)
	if err != nil {
		s.writeStoreError(w, "get settings", err)
		return
	}
	writeJSON(w, http.StatusOK, settings)
}

type platformSettings struct {
	BlockedClasses []string `json:"blocked_classes"`
}

func (s *Service) handleGetPlatformSettings(w http.ResponseWriter, r *http.Request) {
	blocked, err := s.store.GetPlatformSettings(r.Context())
	if err != nil {
		s.writeStoreError(w, "get platform settings", err)
		return
	}
	writeJSON(w, http.StatusOK, platformSettings{BlockedClasses: blocked})
}

func (s *Service) handlePutPlatformSettings(w http.ResponseWriter, r *http.Request) {
	var req platformSettings
	if !decodeBody(w, r, &req) {
		return
	}
	req.BlockedClasses = FilterContentClasses(req.BlockedClasses)

	if err := s.store.UpsertPlatformSettings(r.Context(), req.BlockedClasses); err != nil {
		s.writeStoreError(w, "upsert platform settings", err)
		return
	}
	writeJSON(w, http.StatusOK, req)
}

type reclassifyRequest struct {
	All      bool     `json:"all"`
	Classes  []string `json:"classes"`
	Priority int      `json:"priority"`
}

type reclassifyResponse struct {
	Enqueued int `json:"enqueued"`
	// Rejudged is how many of those will actually reach the vision model. It is
	// reported separately because it has been zero while Enqueued was not: the
	// worker reuses a verdict whose model and version still match, so a resweep
	// that holds the version steady used to drain without judging anything.
	Rejudged int      `json:"rejudged"`
	Version  int      `json:"version"`
	All      bool     `json:"all"`
	Classes  []string `json:"classes,omitempty"`
}

// handleReclassify re-queues rows judged under an older taxonomy. An empty body
// takes exactly the stale ones; {"all": true} forces every classified emote;
// {"classes": [...]} takes every row carrying one of those classes whatever its
// version, which is what a narrowed class definition needs re-judged.
func (s *Service) handleReclassify(w http.ResponseWriter, r *http.Request) {
	var req reclassifyRequest
	if r.ContentLength > 0 && !decodeBody(w, r, &req) {
		return
	}

	// Filtering on a name no emote can carry would silently enqueue nothing.
	classes := FilterContentClasses(req.Classes)
	if len(req.Classes) > 0 && len(classes) == 0 {
		writeError(w, http.StatusBadRequest, "classes must name known content classes")
		return
	}

	enqueued, err := s.store.EnqueueClassified(r.Context(), ClassifierVersion, req.Priority, req.All, classes)
	if err != nil {
		s.writeStoreError(w, "reclassify", err)
		return
	}

	s.logger.Info("reclassification queued",
		"enqueued", enqueued, "rejudged", enqueued, "version", ClassifierVersion, "all", req.All, "classes", classes)
	writeJSON(w, http.StatusOK, reclassifyResponse{
		Enqueued: enqueued, Rejudged: enqueued, Version: ClassifierVersion, All: req.All, Classes: classes,
	})
}

// handleReflash starts the flash backfill and answers with what it will chew
// through. The run takes tens of minutes of local CPU, so the request does not
// wait for it; it is idempotent, so an interrupted one is resumed by asking
// again.
func (s *Service) handleReflash(w http.ResponseWriter, r *http.Request) {
	if s.worker.objects == nil {
		writeError(w, http.StatusServiceUnavailable, ErrObjectStoreUnavailable.Error())
		return
	}
	if !s.reflashing.CompareAndSwap(false, true) {
		writeError(w, http.StatusConflict, "flash backfill already running")
		return
	}

	targets, err := s.store.FlashBackfillTargets(r.Context())
	if err != nil {
		s.reflashing.Store(false)
		s.writeStoreError(w, "flash backfill targets", err)
		return
	}

	go func() {
		defer s.reflashing.Store(false)
		stats, err := s.worker.Reflash(s.jobContext(), targets)
		if err != nil {
			s.logger.Error("flash backfill stopped early",
				"err", err, "measured", stats.Measured, "failed", stats.Failed)
			return
		}
		s.logger.Info("flash backfill finished",
			"emotes", stats.Emotes, "measured", stats.Measured, "failed", stats.Failed)
	}()

	writeJSON(w, http.StatusAccepted, map[string]any{"status": "started", "emotes": len(targets)})
}

func (s *Service) handleListRules(w http.ResponseWriter, r *http.Request) {
	streamerID, ok := parseStreamer(w, r.URL.Query().Get("streamer_id"))
	if !ok {
		return
	}
	rules, err := s.store.ListRules(r.Context(), streamerID)
	if err != nil {
		s.writeStoreError(w, "list rules", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rules": rules})
}

type createRuleRequest struct {
	StreamerID string   `json:"streamer_id"`
	RuleText   string   `json:"rule_text"`
	Threshold  *float64 `json:"threshold"`
}

// handleCreateRule stores the rule as a draft and returns the preview. Nothing
// is enforced until the streamer confirms a subset of these candidates.
func (s *Service) handleCreateRule(w http.ResponseWriter, r *http.Request) {
	var req createRuleRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if req.RuleText == "" {
		writeError(w, http.StatusBadRequest, "rule_text is required")
		return
	}
	streamerID, ok := parseStreamer(w, req.StreamerID)
	if !ok {
		return
	}

	rule, err := s.store.CreateRule(r.Context(), streamerID, req.RuleText, req.Threshold)
	if err != nil {
		s.writeStoreError(w, "create rule", err)
		return
	}

	candidates, err := s.matcher.Candidates(r.Context(), rule.RuleText, rule.Threshold)
	if err != nil {
		s.logger.Error("rule preview failed", "rule_id", rule.ID, "err", err)
		writeJSON(w, http.StatusOK, map[string]any{"rule": rule, "preview_error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rule": rule, "candidates": candidates})
}

func (s *Service) handlePreviewRule(w http.ResponseWriter, r *http.Request) {
	rule, err := s.store.GetRule(r.Context(), chi.URLParam(r, "ruleID"))
	if err != nil {
		s.writeStoreError(w, "get rule", err)
		return
	}
	candidates, err := s.matcher.Candidates(r.Context(), rule.RuleText, rule.Threshold)
	if err != nil {
		s.logger.Error("rule preview failed", "rule_id", rule.ID, "err", err)
		writeError(w, http.StatusBadGateway, "rule preview failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rule": rule, "candidates": candidates})
}

type confirmRuleRequest struct {
	Provider string   `json:"provider"`
	EmoteIDs []string `json:"emote_ids"`
}

func (s *Service) handleConfirmRule(w http.ResponseWriter, r *http.Request) {
	var req confirmRuleRequest
	if !decodeBody(w, r, &req) {
		return
	}
	ruleID := chi.URLParam(r, "ruleID")
	if err := s.store.ConfirmRule(r.Context(), ruleID, req.Provider, req.EmoteIDs); err != nil {
		s.writeStoreError(w, "confirm rule", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"blocked": len(req.EmoteIDs)})
}

func (s *Service) handleDeleteRule(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteRule(r.Context(), chi.URLParam(r, "ruleID")); err != nil {
		s.writeStoreError(w, "delete rule", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
