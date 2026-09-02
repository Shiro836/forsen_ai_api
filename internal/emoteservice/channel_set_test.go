package emoteservice

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"app/pkg/seventv"
)

type fakeChannelSource struct {
	user *seventv.TwitchUser
	set  *seventv.EmoteSet
	err  error
}

func (f fakeChannelSource) GetTwitchUser(context.Context, string) (*seventv.TwitchUser, error) {
	return f.user, f.err
}

func (f fakeChannelSource) GetEmoteSet(context.Context, string) (*seventv.EmoteSet, error) {
	return f.set, f.err
}

func channelSetService(source ChannelSetSource) *Service {
	return &Service{
		logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		cfg:      &Config{SharedSecret: "secret"},
		channels: source,
	}
}

func getChannelSet(t *testing.T, svc *Service, token string) (int, ChannelSet) {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, "/v1/channel-set?twitch_id=22484632", nil)
	req.Header.Set(AuthHeader, token)
	rec := httptest.NewRecorder()

	svc.Router().ServeHTTP(rec, req)

	var out ChannelSet
	_ = json.Unmarshal(rec.Body.Bytes(), &out)

	return rec.Code, out
}

func TestHandleChannelSet(t *testing.T) {
	t.Parallel()

	svc := channelSetService(fakeChannelSource{user: &seventv.TwitchUser{
		Username:   "forsen",
		EmoteSetID: "set1",
		Set: seventv.EmoteSet{Emotes: []seventv.ActiveEmote{
			{EmoteID: "abc", Name: "forsenE", BaseName: "PogChamp", ZeroWidth: true},
		}},
	}})

	code, set := getChannelSet(t, svc, "secret")
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	if !set.Linked || set.Username != "forsen" || len(set.Emotes) != 1 {
		t.Fatalf("unexpected set: %+v", set)
	}
	if set.Emotes[0].BaseName != "PogChamp" || !set.Emotes[0].ZeroWidth {
		t.Fatalf("unexpected emote: %+v", set.Emotes[0])
	}
}

func TestHandleChannelSetNoAccount(t *testing.T) {
	t.Parallel()

	code, set := getChannelSet(t, channelSetService(fakeChannelSource{err: seventv.ErrNoAccount}), "secret")
	if code != http.StatusOK {
		t.Fatalf("a channel without a 7tv account is not an error, got status %d", code)
	}
	if set.Linked || len(set.Emotes) != 0 {
		t.Fatalf("unexpected set: %+v", set)
	}
}

func TestHandleChannelSetNeedsToken(t *testing.T) {
	t.Parallel()

	if code, _ := getChannelSet(t, channelSetService(fakeChannelSource{}), "wrong"); code != http.StatusUnauthorized {
		t.Fatalf("status %d", code)
	}
}

func TestHandleGlobalSet(t *testing.T) {
	t.Parallel()

	svc := channelSetService(fakeChannelSource{set: &seventv.EmoteSet{
		ID:     "global",
		Emotes: []seventv.ActiveEmote{{EmoteID: "abc", Name: "PogChamp", BaseName: "PogChamp"}},
	}})

	req := httptest.NewRequest(http.MethodGet, "/v1/global-set", nil)
	req.Header.Set(AuthHeader, "secret")
	rec := httptest.NewRecorder()
	svc.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}

	var out EmoteSetResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.SetID != "global" || len(out.Emotes) != 1 || out.Emotes[0].Name != "PogChamp" {
		t.Fatalf("unexpected global set: %+v", out)
	}
}

func searchService(matcher *RuleMatcher) *Service {
	svc := channelSetService(fakeChannelSource{})
	svc.matcher = matcher

	return svc
}

func doSearch(t *testing.T, svc *Service, query string) (int, []Candidate) {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, "/v1/search?q="+url.QueryEscape(query), nil)
	req.Header.Set(AuthHeader, "secret")
	rec := httptest.NewRecorder()

	svc.Router().ServeHTTP(rec, req)

	var out struct {
		Results []Candidate `json:"results"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)

	return rec.Code, out.Results
}

func TestHandleSemanticSearch(t *testing.T) {
	t.Parallel()

	embedder := &fakeEmbedder{vectors: map[string][]float32{"crying anime girl": {1, 0}}}
	source := &fakeVectors{emotes: []fakeEmote{
		{id: "far", name: "pepeLaugh", image: []float32{0, 1}},
		{id: "near", name: "sadKanna", image: []float32{1, 0}},
	}}

	code, results := doSearch(t, searchService(testMatcher(embedder, source)), "crying anime girl")
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	if len(results) != 2 {
		t.Fatalf("a search has no cutoff, want both emotes, got %+v", results)
	}
	if results[0].EmoteID != "near" || results[1].EmoteID != "far" {
		t.Fatalf("results are not ranked by cosine: %+v", results)
	}
	if results[0].Score <= results[1].Score {
		t.Fatalf("scores do not decrease: %+v", results)
	}
}

func TestHandleSemanticSearchRequiresQuery(t *testing.T) {
	t.Parallel()

	if code, _ := doSearch(t, searchService(testMatcher(&fakeEmbedder{}, &fakeVectors{})), ""); code != http.StatusBadRequest {
		t.Fatalf("status %d", code)
	}
}

// With no embedder the endpoint must fail loudly: the caller keeps name search
// working and says so, rather than silently serving half an answer.
func TestHandleSemanticSearchWithoutEmbedder(t *testing.T) {
	t.Parallel()

	code, results := doSearch(t, searchService(testMatcher(nil, &fakeVectors{})), "anime")
	if code != http.StatusServiceUnavailable {
		t.Fatalf("status %d", code)
	}
	if len(results) != 0 {
		t.Fatalf("unexpected results: %+v", results)
	}
}

// The version constant is what decides which stored rows are stale, so a bump
// that forgets one of its two consumers is caught here.
func TestClassifierVersionIsCurrentTaxonomy(t *testing.T) {
	t.Parallel()

	if ClassifierVersion < 3 {
		t.Fatalf("the content-class taxonomy is generation 3, got %d", ClassifierVersion)
	}

	now := time.Now()
	old := &Emote{ClassifiedAt: &now, ClassifierVersion: ClassifierVersion - 1}
	current := &Emote{ClassifiedAt: &now, ClassifierVersion: ClassifierVersion}

	if !old.Stale(ClassifierVersion) || current.Stale(ClassifierVersion) {
		t.Fatal("staleness must follow the version constant")
	}
}

func TestHandleEnqueueSetRequiresTarget(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodPost, "/v1/enqueue-set", strings.NewReader(`{}`))
	req.Header.Set(AuthHeader, "secret")
	rec := httptest.NewRecorder()
	channelSetService(fakeChannelSource{}).Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("a request naming neither a set nor a channel must be rejected, got %d", rec.Code)
	}
}
