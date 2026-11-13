package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"app/db"
	"app/pkg/ctxstore"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func bdFormInner(t *testing.T, body string) string {
	t.Helper()
	_, rest, ok := strings.Cut(body, `<form id="bd-page"`)
	require.True(t, ok)
	inner, after, ok := strings.Cut(rest, `</form>`)
	require.True(t, ok)
	assert.Contains(t, after, `id="bd-providers"`)
	return inner
}

func TestBDProvidersStayOutOfTheSaveForm(t *testing.T) {
	open := &bdPage{Providers: bdProviderList(db.DonationProviderLinks{}), Save: saveStatus{ID: bdSaveStatusID}}
	body := getString("bits_donations.html", open)
	form := bdFormInner(t, body)

	assert.Contains(t, form, `hx-post="/bits-donations"`)
	assert.Contains(t, form, `hx-post="/form/changes"`)
	assert.Contains(t, form, `name="order"`)
	assert.Contains(t, form, `style="display:contents"`)
	assert.Contains(t, form, `</div><div class="flex flex-wrap items-start gap-6" style="grid-column:1 / -1;grid-row:2">`)
	assert.Contains(t, form, `</div><div class="sticky bottom-0 z-20 flex items-center gap-4 border-t`)
	assert.True(t, strings.HasSuffix(form, "</div>"))
	assert.Contains(t, body, `</form><div id="bd-providers"`)
	for _, needle := range []string{`name="token"`, `/streamelements`, `/streamlabs/connect`, `/donationalerts/connect`, `id="bd-providers"`, `target="_blank"`} {
		assert.NotContains(t, form, needle, needle)
	}
	assert.NotContains(t, body, "hx-include")
	assert.Contains(t, body, `action="/streamelements"`)
	assert.Contains(t, body, `hx-post="/streamelements"`)
	assert.Contains(t, body, `href="/streamlabs/connect"`)
	assert.Contains(t, body, `href="/donationalerts/connect"`)
	assert.Equal(t, 2, strings.Count(body, `target="_blank"`))
	assert.NotContains(t, form, ">how<")
	assert.NotContains(t, form, `id="se-how"`)
	assert.Equal(t, 1, strings.Count(body, ">how<"))
	assert.Contains(t, body, `<h2 class="text-xl font-bold">Donation providers</h2>`)
	assert.Contains(t, body, `id="bd-providers" class="flex min-w-0 flex-col gap-3"`)
	assert.Contains(t, body, "anchor-name:--bd-providers")
	assert.Contains(t, body, `id="bd-provider-card"`)
	_, how, ok := strings.Cut(body, `<form method="post" action="/streamelements"`)
	require.True(t, ok)
	assert.Contains(t, how, ">how<")
	howBtn := body[strings.LastIndex(body[:strings.Index(body, ">how<")], "<button"):strings.Index(body, ">how<")]
	assert.Contains(t, howBtn, `popovertarget="se-how"`)
	assert.Contains(t, howBtn, `hx-get="/guide/streamelements"`)
	assert.Contains(t, howBtn, `hx-select="#guide"`)
	assert.Contains(t, howBtn, `hx-target="#se-how-body"`)
	assert.Contains(t, howBtn, `hx-trigger="click once"`)
	assert.NotContains(t, howBtn, "onclick")
	assert.NotContains(t, howBtn, "getElementById")
	assert.NotContains(t, howBtn, "showModal")
	assert.NotContains(t, howBtn, "hx-push-url")
	assert.NotContains(t, howBtn, "#tab-content")
	assert.Contains(t, body, `<div id="se-how" popover`)
	assert.Contains(t, body, `popovertargetaction="hide"`)
	assert.NotContains(t, body, "<dialog")
	assert.NotContains(t, body, "left:106rem")
	assert.NotContains(t, body, "onclick")
	assert.Contains(t, body, "grid-template-columns:26rem 26rem minmax(0,1fr)")
	assert.Contains(t, body, "anchor-name:--bd-page")
	assert.Contains(t, body, "position-anchor:--bd-providers")
	assert.Contains(t, body, "top:anchor(bottom)")
	assert.Contains(t, body, "right:anchor(--bd-page right)")
	assert.Contains(t, body, "w-96")
	assert.Contains(t, body, "left:auto")
	assert.Contains(t, body, "bottom:auto")
	assert.NotContains(t, body, "mx-auto")
	assert.Contains(t, body, `<input type="text" name="token"`)
	assert.Contains(t, body, "select-all [&:not(:placeholder-shown)]:blur-sm")
	assert.Contains(t, body, `placeholder="paste token"`)
	assert.NotContains(t, body, `type="password"`)
	assert.NotContains(t, body, "text-security")
	assert.NotContains(t, body, `name="token" value=`)
	assert.NotContains(t, body, "OAuth")
	assert.NotContains(t, body, "socket")

	for _, provider := range []string{"streamlabs", "donationalerts"} {
		assert.Contains(t, body, `hx-get="/donation-providers/`+provider+`" hx-trigger="visibilitychange from:document"`)
	}
	assert.NotContains(t, body, `hx-get="/donation-providers/streamelements"`)

	connected := &bdPage{Providers: bdProviderList(db.DonationProviderLinks{
		StreamElements: true,
		Streamlabs:     true,
		DonationAlerts: true,
	})}
	body = getString("bits_donations.html", connected)
	assert.NotContains(t, body, `name="token"`)
	assert.NotContains(t, body, `href="/streamlabs/connect"`)
	assert.NotContains(t, body, `href="/donationalerts/connect"`)
	assert.NotContains(t, body, "saved-access-token")
	assert.Equal(t, 3, strings.Count(body, ">connected<"))
	assert.Equal(t, 0, strings.Count(body, ">how<"))
	assert.Equal(t, 1, strings.Count(body, `<div id="se-how" popover`))
	for _, provider := range []string{"streamelements", "streamlabs", "donationalerts"} {
		assert.Contains(t, body, `hx-post="/donation-providers/`+provider+`/disconnect"`)
	}
}

func TestBDProviderFor(t *testing.T) {
	links := db.DonationProviderLinks{Streamlabs: true}

	sl := bdProviderFor("streamlabs", links)
	assert.True(t, sl.Connected)
	assert.True(t, sl.Live)
	assert.Equal(t, "/streamlabs/connect", sl.Href)

	se := bdProviderFor("streamelements", links)
	assert.False(t, se.Connected)
	assert.True(t, se.Token)
	assert.False(t, se.Live)

	assert.False(t, validDonationProvider("twitch"))
	assert.True(t, validDonationProvider(db.ProviderDonationAlerts))
}

func TestDonationProviderHandlersValidateProvider(t *testing.T) {
	id := uuid.MustParse("0199f2b3-73f6-78a3-b3aa-ddb84b848502")
	api := &API{}

	req := httptest.NewRequest(http.MethodGet, "/donation-providers/twitch", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("provider", "twitch")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	req = req.WithContext(ctxstore.WithUser(req.Context(), &db.User{ID: id}))

	rec := httptest.NewRecorder()
	api.donationProviderBlock(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	rec = httptest.NewRecorder()
	api.donationProviderDisconnect(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	req = httptest.NewRequest(http.MethodGet, "/donation-providers/streamlabs", nil)
	rec = httptest.NewRecorder()
	api.donationProviderBlock(rec, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)

	rec = httptest.NewRecorder()
	api.donationProviderDisconnect(rec, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestProviderEndpoints(t *testing.T) {
	assert.Equal(t, "https://streamlabs.com/api/v2.0/authorize", streamlabsAuthorizeURL)
	assert.Equal(t, "https://streamlabs.com/api/v2.0/token", streamlabsTokenURL)
	assert.NotContains(t, streamlabsTokenURL, "/socket/token")
	assert.Equal(t, "https://beta2.forsen.fun/streamlabs/callback", streamlabsRedirectURI)
	assert.NotContains(t, streamlabsRedirectURI, "donationalerts")
	assert.Equal(t, "https://www.donationalerts.com/oauth/authorize", donationAlertsAuthorizeURL)
	assert.Equal(t, "https://www.donationalerts.com/oauth/token", donationAlertsTokenURL)
	assert.Equal(t, "https://beta2.forsen.fun/donationalerts/callback", donationAlertsRedirectURI)
	assert.NotEqual(t, "https://forsen.fun/donationalerts/callback", donationAlertsRedirectURI)
	assert.Equal(t, []string{"oauth-user-show", "oauth-donation-subscribe", "oauth-donation-index"}, donationAlertsScopes)
}

func TestStreamlabsStartUsesBetaRedirect(t *testing.T) {
	api := &API{streamlabs: ProviderApp{ClientID: "cid", ClientSecret: "topsecret"}}
	req := httptest.NewRequest(http.MethodGet, "/streamlabs/connect", nil)
	rec := httptest.NewRecorder()
	api.streamlabsConnect(rec, req)

	require.Equal(t, http.StatusFound, rec.Code)
	loc, err := url.Parse(rec.Header().Get("Location"))
	require.NoError(t, err)
	assert.Equal(t, "https", loc.Scheme)
	assert.Equal(t, "streamlabs.com", loc.Host)
	assert.Equal(t, "/api/v2.0/authorize", loc.Path)
	assert.NotContains(t, loc.Path, "v1.0")
	q := loc.Query()
	assert.Equal(t, "code", q.Get("response_type"))
	assert.Equal(t, "cid", q.Get("client_id"))
	assert.Equal(t, streamlabsRedirectURI, q.Get("redirect_uri"))
	assert.Equal(t, "socket.token", q.Get("scope"))
	assert.NotContains(t, rec.Header().Get("Location"), "topsecret")

	var state *http.Cookie
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == cookieOAuthState {
			state = ck
		}
	}
	require.NotNil(t, state)
	assert.Equal(t, q.Get("state"), state.Value)
	assert.True(t, state.HttpOnly)
	assert.True(t, state.Secure)
	assert.Equal(t, http.SameSiteLaxMode, state.SameSite)
}

func TestDonationAlertsStartUsesBetaRedirect(t *testing.T) {
	api := &API{donationAlerts: ProviderApp{ClientID: "cid", ClientSecret: "topsecret"}}
	req := httptest.NewRequest(http.MethodGet, "/donationalerts/connect", nil)
	rec := httptest.NewRecorder()
	api.donationAlertsConnect(rec, req)

	require.Equal(t, http.StatusFound, rec.Code)
	loc, err := url.Parse(rec.Header().Get("Location"))
	require.NoError(t, err)
	assert.Equal(t, "www.donationalerts.com", loc.Host)
	assert.Equal(t, "/oauth/authorize", loc.Path)
	q := loc.Query()
	assert.Equal(t, "code", q.Get("response_type"))
	assert.Equal(t, donationAlertsRedirectURI, q.Get("redirect_uri"))
	assert.Equal(t, strings.Join(donationAlertsScopes, " "), q.Get("scope"))
	assert.NotContains(t, rec.Header().Get("Location"), "topsecret")
	assert.NotEqual(t, "https://forsen.fun/donationalerts/callback", q.Get("redirect_uri"))
}

func TestProviderStartWithoutApp(t *testing.T) {
	api := &API{}
	req := httptest.NewRequest(http.MethodGet, "/streamlabs/connect", nil)
	rec := httptest.NewRecorder()
	api.streamlabsConnect(rec, req)
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Contains(t, rec.Body.String(), "not set up")
	assert.Empty(t, rec.Header().Get("Location"))
}

func TestProviderCallbackChecksState(t *testing.T) {
	api := &API{streamlabs: ProviderApp{ClientID: "cid", ClientSecret: "topsecret"}}
	req := httptest.NewRequest(http.MethodGet, "/streamlabs/callback?code=abc&state=nope", nil)
	req.AddCookie(stateCookie("expected", 60))
	rec := httptest.NewRecorder()
	api.streamlabsCallback(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "state mismatch, start again")
	assert.NotContains(t, rec.Body.String(), "topsecret")
	assert.NotContains(t, rec.Body.String(), "abc")
	assert.Equal(t, "no-referrer", rec.Header().Get("Referrer-Policy"))
}

func TestProviderCallbackRequiresUser(t *testing.T) {
	api := &API{donationAlerts: ProviderApp{ClientID: "cid", ClientSecret: "topsecret"}}
	req := httptest.NewRequest(http.MethodGet, "/donationalerts/callback?code=abc&state=same", nil)
	req.AddCookie(stateCookie("same", 60))
	rec := httptest.NewRecorder()
	api.donationAlertsCallback(rec, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.NotContains(t, rec.Body.String(), "topsecret")
}

func TestProviderCallbackDenied(t *testing.T) {
	api := &API{streamlabs: ProviderApp{ClientID: "cid", ClientSecret: "topsecret"}}
	req := httptest.NewRequest(http.MethodGet, "/streamlabs/callback?error=access_denied&state=same", nil)
	req.AddCookie(stateCookie("same", 60))
	req = req.WithContext(ctxstore.WithUser(req.Context(), &db.User{ID: uuid.MustParse("0199f2b3-73f6-78a3-b3aa-ddb84b848502")}))
	rec := httptest.NewRecorder()
	api.streamlabsCallback(rec, req)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Contains(t, rec.Body.String(), "not connected")
	assert.NotContains(t, rec.Body.String(), "access_denied")
}

func TestStreamElementsEmptyTokenDoesNotSave(t *testing.T) {
	api := &API{}
	req := httptest.NewRequest(http.MethodPost, "/streamelements", strings.NewReader("token=+"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = req.WithContext(ctxstore.WithUser(req.Context(), &db.User{ID: uuid.MustParse("0199f2b3-73f6-78a3-b3aa-ddb84b848502")}))
	rec := httptest.NewRecorder()
	api.streamElementsConnect(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, "empty")
	assert.Contains(t, body, `hx-post="/streamelements"`)
	assert.Contains(t, body, ">how<")
	assert.Contains(t, body, `popovertarget="se-how"`)
	assert.Contains(t, body, "select-all [&:not(:placeholder-shown)]:blur-sm")
	assert.NotContains(t, body, "<dialog")
	assert.NotContains(t, body, "onclick")
	assert.NotContains(t, body, "showModal")
	assert.NotContains(t, body, "hx-push-url")
	assert.NotContains(t, body, "#tab-content")
	assert.NotContains(t, body, ">connected<")
	assert.NotContains(t, body, "/bits-donations")
}

func TestFetchProviderTokenPostsForm(t *testing.T) {
	var path, body, contentType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		contentType = r.Header.Get("Content-Type")
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		if r.Method != http.MethodPost || r.URL.Path != "/token" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"access_token":"atk","refresh_token":"rtk","scope":"socket.token"}`))
	}))
	t.Cleanup(srv.Close)

	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("client_id", "cid")
	form.Set("client_secret", "topsecret")
	form.Set("redirect_uri", streamlabsRedirectURI)
	form.Set("code", "the-code")

	tok, err := fetchProviderToken(context.Background(), srv.Client(), srv.URL+"/token", form)
	require.NoError(t, err)
	assert.Equal(t, "/token", path)
	assert.NotContains(t, path, "socket")
	assert.Contains(t, contentType, "application/x-www-form-urlencoded")
	parsed, err := url.ParseQuery(body)
	require.NoError(t, err)
	assert.Equal(t, "authorization_code", parsed.Get("grant_type"))
	assert.Equal(t, "cid", parsed.Get("client_id"))
	assert.Equal(t, "topsecret", parsed.Get("client_secret"))
	assert.Equal(t, streamlabsRedirectURI, parsed.Get("redirect_uri"))
	assert.Equal(t, "the-code", parsed.Get("code"))
	assert.Equal(t, "atk", tok.AccessToken)
	assert.Equal(t, "rtk", tok.RefreshToken)
	assert.Equal(t, []string{"socket.token"}, tok.Scopes)

	assert.Nil(t, storedRefresh(false, tok.RefreshToken))
	kept := storedRefresh(true, tok.RefreshToken)
	require.NotNil(t, kept)
	assert.Equal(t, "rtk", *kept)
	assert.Nil(t, storedRefresh(true, ""))
}

func TestScopeLists(t *testing.T) {
	assert.Equal(t, []string{"a", "b"}, parseScopeList(json.RawMessage(`"a b"`)))
	assert.Equal(t, []string{"a", "b"}, parseScopeList(json.RawMessage(`["a","b"]`)))
	assert.Nil(t, parseScopeList(nil))
	assert.Nil(t, parseScopeList(json.RawMessage(`null`)))
	assert.Equal(t, donationAlertsScopes, scopesToStore(nil, donationAlertsScopes))
	assert.Equal(t, []string{}, scopesToStore(nil, nil))
	assert.Equal(t, []string{"socket.token"}, scopesToStore([]string{"socket.token"}, nil))
}
