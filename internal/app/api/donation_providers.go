package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"app/db"
	"app/internal/app/donations"
	"app/pkg/ctxstore"

	"github.com/go-chi/chi/v5"
)

const (
	// Registered on the beta apps. Streamlabs also has
	// https://forsen.fun/donationalerts/callback on file; that URI is a
	// different DonationAlerts app and is not this redirect.
	streamlabsRedirectURI     = "https://beta2.forsen.fun/streamlabs/callback"
	donationAlertsRedirectURI = "https://beta2.forsen.fun/donationalerts/callback"

	streamlabsAuthorizeURL = "https://streamlabs.com/api/v2.0/authorize"
	streamlabsTokenURL     = "https://streamlabs.com/api/v2.0/token"
	streamlabsScope        = "socket.token donations.read"

	donationAlertsAuthorizeURL = "https://www.donationalerts.com/oauth/authorize"
	donationAlertsTokenURL     = "https://www.donationalerts.com/oauth/token"

	donationProviderTokenMax = 4096
)

var donationAlertsScopes = []string{
	"oauth-user-show",
	"oauth-donation-subscribe",
	"oauth-donation-index",
}

// ProviderApp is one donation provider's app credentials from cfg.
type ProviderApp struct {
	ClientID     string
	ClientSecret string
}

type bdProvider struct {
	ID          string
	Name        string
	Connected   bool
	Token       bool
	Live        bool
	Href        string
	Post        string
	Error       string
	Status      string
	StatusError string
	Expiry      string
}

func bdProviderList(links db.DonationProviderLinks) []bdProvider {
	state := func(provider string) db.DonationProviderState {
		if links.State == nil {
			return db.DonationProviderState{}
		}
		return links.State[provider]
	}

	se := state(db.ProviderStreamElements)
	sl := state(db.ProviderStreamlabs)
	da := state(db.ProviderDonationAlerts)

	return []bdProvider{
		{
			ID: db.ProviderStreamElements, Name: "StreamElements",
			Connected: links.StreamElements, Token: !links.StreamElements, Post: "/streamelements",
			// Polling while the paste form is open would wipe what is typed.
			Live:        links.StreamElements,
			Status:      se.Status,
			StatusError: se.Error,
			Expiry:      tokenExpiry(se.ExpiresAt),
		},
		{
			ID: db.ProviderStreamlabs, Name: "Streamlabs",
			Connected: links.Streamlabs, Live: true, Href: "/streamlabs/connect",
			Status: sl.Status, StatusError: sl.Error,
		},
		{
			ID: db.ProviderDonationAlerts, Name: "DonationAlerts",
			Connected: links.DonationAlerts, Live: true, Href: "/donationalerts/connect",
			Status: da.Status, StatusError: da.Error,
		},
	}
}

// tokenExpiry warns before a token that cannot be refreshed runs out.
func tokenExpiry(expiresAt *time.Time) string {
	if expiresAt == nil {
		return ""
	}
	remaining := time.Until(*expiresAt)
	if remaining <= 0 {
		return "expired"
	}
	if days := int(remaining.Hours() / 24); days <= 14 {
		return fmt.Sprintf("expires in %dd", days)
	}
	return ""
}

func validDonationProvider(provider string) bool {
	switch provider {
	case db.ProviderStreamElements, db.ProviderStreamlabs, db.ProviderDonationAlerts:
		return true
	}
	return false
}

func bdProviderFor(provider string, links db.DonationProviderLinks) bdProvider {
	for _, p := range bdProviderList(links) {
		if p.ID == provider {
			return p
		}
	}
	return bdProvider{}
}

func (api *API) streamElementsConnect(w http.ResponseWriter, r *http.Request) {
	user := ctxstore.GetUser(r.Context())
	if user == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "failed to parse form", http.StatusBadRequest)
		return
	}

	block := bdProvider{ID: "streamelements", Name: "StreamElements", Token: true, Post: "/streamelements"}
	token := strings.TrimSpace(r.Form.Get("token"))
	if token == "" || len(token) > donationProviderTokenMax {
		block.Error = "empty"
		if len(token) > donationProviderTokenMax {
			block.Error = "too long"
		}
		_ = html.ExecuteTemplate(w, "bd-provider", block)
		return
	}

	expiresAt, valid := donations.ParseStreamElementsToken(token)
	if !valid {
		block.Error = "not a JWT"
		_ = html.ExecuteTemplate(w, "bd-provider", block)
		return
	}

	var expires *time.Time
	if !expiresAt.IsZero() {
		expires = &expiresAt
	}

	if err := api.db.UpsertDonationProvider(r.Context(), user.ID, db.ProviderStreamElements, token, nil, []string{}, expires); err != nil {
		api.logger.Error("streamelements connect failed", "err", err, "user", user.ID)
		block.Error = "failed to save"
		_ = html.ExecuteTemplate(w, "bd-provider", block)
		return
	}

	block.Connected = true
	block.Token = false
	block.Live = true
	_ = html.ExecuteTemplate(w, "bd-provider", block)
}

func (api *API) donationProviderBlock(w http.ResponseWriter, r *http.Request) {
	user := ctxstore.GetUser(r.Context())
	if user == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	provider := chi.URLParam(r, "provider")
	if !validDonationProvider(provider) {
		http.Error(w, "unknown provider", http.StatusBadRequest)
		return
	}

	links, err := api.db.ConnectedDonationProviders(r.Context(), user.ID)
	if err != nil {
		api.logger.Error("load donation providers failed", "err", err, "user", user.ID)
		http.Error(w, "failed to load providers", http.StatusInternalServerError)
		return
	}

	if err := html.ExecuteTemplate(w, "bd-provider", bdProviderFor(provider, links)); err != nil {
		api.logger.Error("render donation provider failed", "err", err, "provider", provider, "user", user.ID)
	}
}

func (api *API) donationProviderDisconnect(w http.ResponseWriter, r *http.Request) {
	user := ctxstore.GetUser(r.Context())
	if user == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	provider := chi.URLParam(r, "provider")
	if !validDonationProvider(provider) {
		http.Error(w, "unknown provider", http.StatusBadRequest)
		return
	}

	if err := api.db.DeleteDonationProvider(r.Context(), user.ID, provider); err != nil {
		api.logger.Error("delete donation provider failed", "err", err, "provider", provider, "user", user.ID)
		http.Error(w, "failed to disconnect", http.StatusInternalServerError)
		return
	}

	if err := html.ExecuteTemplate(w, "bd-provider", bdProviderFor(provider, db.DonationProviderLinks{})); err != nil {
		api.logger.Error("render donation provider failed", "err", err, "provider", provider, "user", user.ID)
	}
}

func (api *API) streamlabsConnect(w http.ResponseWriter, r *http.Request) {
	api.beginProviderOAuth(w, r, api.streamlabs, streamlabsAuthorizeURL, streamlabsRedirectURI, streamlabsScope)
}

func (api *API) donationAlertsConnect(w http.ResponseWriter, r *http.Request) {
	api.beginProviderOAuth(w, r, api.donationAlerts, donationAlertsAuthorizeURL, donationAlertsRedirectURI, strings.Join(donationAlertsScopes, " "))
}

func (api *API) streamlabsCallback(w http.ResponseWriter, r *http.Request) {
	api.finishProviderOAuth(w, r, providerOAuth{
		App:         api.streamlabs,
		TokenURL:    streamlabsTokenURL,
		RedirectURI: streamlabsRedirectURI,
		Provider:    db.ProviderStreamlabs,
		Name:        "Streamlabs",
	})
}

func (api *API) donationAlertsCallback(w http.ResponseWriter, r *http.Request) {
	api.finishProviderOAuth(w, r, providerOAuth{
		App:         api.donationAlerts,
		TokenURL:    donationAlertsTokenURL,
		RedirectURI: donationAlertsRedirectURI,
		Provider:    db.ProviderDonationAlerts,
		Name:        "DonationAlerts",
		KeepRefresh: true,
		Scopes:      donationAlertsScopes,
	})
}

func writeErrPage(w http.ResponseWriter, r *http.Request, code int, message string) {
	w.WriteHeader(code)
	submitPage(w, errPage(r, code, message))
}

func (api *API) beginProviderOAuth(w http.ResponseWriter, r *http.Request, app ProviderApp, authorizeURL, redirectURI, scope string) {
	if app.ClientID == "" || app.ClientSecret == "" {
		writeErrPage(w, r, http.StatusInternalServerError, "not set up")
		return
	}

	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		writeErrPage(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	state := hex.EncodeToString(raw[:])
	http.SetCookie(w, stateCookie(state, int(oauthStateTTL.Seconds())))

	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", app.ClientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("scope", scope)
	q.Set("state", state)
	http.Redirect(w, r, authorizeURL+"?"+q.Encode(), http.StatusFound)
}

type providerOAuth struct {
	App         ProviderApp
	TokenURL    string
	RedirectURI string
	Provider    string
	Name        string
	KeepRefresh bool
	Scopes      []string
}

func (api *API) finishProviderOAuth(w http.ResponseWriter, r *http.Request, p providerOAuth) {
	w.Header().Set("Referrer-Policy", "no-referrer")

	stateCk, err := r.Cookie(cookieOAuthState)
	if err != nil || subtle.ConstantTimeCompare([]byte(stateCk.Value), []byte(r.URL.Query().Get("state"))) != 1 {
		writeErrPage(w, r, http.StatusBadRequest, "state mismatch, start again")
		return
	}
	http.SetCookie(w, stateCookie("", -1))

	user := ctxstore.GetUser(r.Context())
	if user == nil {
		writeErrPage(w, r, http.StatusUnauthorized, "unauthorized")
		return
	}
	if p.App.ClientID == "" || p.App.ClientSecret == "" {
		writeErrPage(w, r, http.StatusInternalServerError, "not set up")
		return
	}
	if r.URL.Query().Get("error") != "" || r.URL.Query().Get("code") == "" {
		writeErrPage(w, r, http.StatusBadRequest, "not connected")
		return
	}

	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("client_id", p.App.ClientID)
	form.Set("client_secret", p.App.ClientSecret)
	form.Set("redirect_uri", p.RedirectURI)
	form.Set("code", r.URL.Query().Get("code"))

	tok, err := fetchProviderToken(r.Context(), api.providerHTTP, p.TokenURL, form)
	if err != nil {
		api.logger.Error("donation provider connect failed", "err", err, "provider", p.Provider, "user", user.ID)
		writeErrPage(w, r, http.StatusBadGateway, "not connected")
		return
	}

	refresh := storedRefresh(p.KeepRefresh, tok.RefreshToken)
	if p.KeepRefresh && refresh == nil {
		api.logger.Error("donation provider connect failed", "err", "missing refresh token", "provider", p.Provider, "user", user.ID)
		writeErrPage(w, r, http.StatusBadGateway, "not connected")
		return
	}

	var expires *time.Time
	if tok.ExpiresIn > 0 {
		next := time.Now().UTC().Add(time.Duration(tok.ExpiresIn) * time.Second)
		expires = &next
	}

	if err := api.db.UpsertDonationProvider(r.Context(), user.ID, p.Provider, tok.AccessToken, refresh, scopesToStore(tok.Scopes, p.Scopes), expires); err != nil {
		api.logger.Error("donation provider connect failed", "err", err, "provider", p.Provider, "user", user.ID)
		writeErrPage(w, r, http.StatusInternalServerError, "failed to save")
		return
	}

	api.nav(func(r *http.Request) template.HTML {
		return getHtml("donation_connected.html", p.Name)
	}).ServeHTTP(w, r)
}

type providerToken struct {
	AccessToken  string
	RefreshToken string
	Scopes       []string
	ExpiresIn    int64
}

type oauthTokenResponse struct {
	AccessToken  string          `json:"access_token"`
	RefreshToken string          `json:"refresh_token"`
	ExpiresIn    int64           `json:"expires_in"`
	Scope        json.RawMessage `json:"scope"`
	Scopes       json.RawMessage `json:"scopes"`
}

func fetchProviderToken(ctx context.Context, client *http.Client, tokenURL string, form url.Values) (providerToken, error) {
	if client == nil {
		return providerToken{}, fmt.Errorf("token exchange: no client")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return providerToken{}, fmt.Errorf("token exchange: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := client.Do(req)
	if err != nil {
		return providerToken{}, fmt.Errorf("token exchange: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		return providerToken{}, fmt.Errorf("token exchange status %d", resp.StatusCode)
	}

	var parsed oauthTokenResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&parsed); err != nil || parsed.AccessToken == "" {
		return providerToken{}, fmt.Errorf("token exchange response")
	}

	scopes := parseScopeList(parsed.Scope)
	if len(scopes) == 0 {
		scopes = parseScopeList(parsed.Scopes)
	}
	return providerToken{
		AccessToken:  parsed.AccessToken,
		RefreshToken: parsed.RefreshToken,
		Scopes:       scopes,
		ExpiresIn:    parsed.ExpiresIn,
	}, nil
}

func refreshPointer(refresh string) *string {
	if refresh == "" {
		return nil
	}
	return &refresh
}

func storedRefresh(keep bool, refresh string) *string {
	if !keep {
		return nil
	}
	return refreshPointer(refresh)
}

func scopesToStore(got, fallback []string) []string {
	if len(got) == 0 {
		got = fallback
	}
	if len(got) == 0 {
		return []string{}
	}
	return append([]string(nil), got...)
}

func parseScopeList(raw json.RawMessage) []string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return splitScopeText(text)
	}
	var list []string
	if err := json.Unmarshal(raw, &list); err == nil {
		return compactScopes(list)
	}
	return nil
}

func splitScopeText(text string) []string {
	return compactScopes(strings.FieldsFunc(text, func(r rune) bool {
		return r == ' ' || r == ','
	}))
}

func compactScopes(in []string) []string {
	var out []string
	for _, scope := range in {
		scope = strings.TrimSpace(scope)
		if scope != "" {
			out = append(out, scope)
		}
	}
	return out
}
