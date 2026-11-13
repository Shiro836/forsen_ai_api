package donations

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"app/db"

	"github.com/centrifugal/centrifuge-go"
)

const (
	daCentrifugeURL   = "wss://centrifugo.donationalerts.com/connection/websocket"
	daAPIBase         = "https://www.donationalerts.com"
	daDonationTopic   = "$alerts:donation_"
	daBackfillMaxPage = 3
)

// DonationAlertsApp is the OAuth application used to refresh a streamer's
// access token. The streamer's own token is what the API calls use.
type DonationAlertsApp struct {
	ClientID     string
	ClientSecret string
}

type donationalerts struct {
	logger *slog.Logger
	db     *db.DB
	app    DonationAlertsApp
	client *http.Client
}

func NewDonationAlerts(logger *slog.Logger, database *db.DB, app DonationAlertsApp) Source {
	return &donationalerts{
		logger: logger,
		db:     database,
		app:    app,
		client: &http.Client{Timeout: 15 * time.Second},
	}
}

func (s *donationalerts) Provider() string { return db.ProviderDonationAlerts }

func (s *donationalerts) Run(ctx context.Context, creds Credentials, session Session) error {
	auth := &donationAlertsAuth{source: s, creds: creds}

	user, err := auth.user(ctx)
	if err != nil {
		return err
	}
	if user.ID == 0 || user.SocketConnectionToken == "" {
		return fmt.Errorf("%w: donation alerts user has no socket token", ErrAuth)
	}

	if since, ok := backfillSince(creds); ok {
		s.logger.Info("backfilling donations", "since", since)
		if err := s.backfill(ctx, auth, since, session); err != nil {
			s.logger.Error("donation alerts backfill failed", "err", err)
		}
	}

	config := centrifuge.DefaultConfig()
	config.Name = "baj-ai"

	client := centrifuge.NewJsonClient(daCentrifugeURL, config)
	client.SetToken(user.SocketConnectionToken)

	connected := make(chan struct{}, 1)
	failed := make(chan error, 1)
	disconnected := make(chan string, 1)

	client.OnConnect(daConnectHandler(func(*centrifuge.Client, centrifuge.ConnectEvent) {}))
	client.OnPrivateSub(daPrivateSubHandler(func(_ *centrifuge.Client, ev centrifuge.PrivateSubEvent) (string, error) {
		return auth.channelToken(ctx, ev.ClientID, ev.Channel)
	}))
	client.OnRefresh(daRefreshHandler(func(*centrifuge.Client) (string, error) {
		return auth.connectionToken(ctx)
	}))
	client.OnError(daErrorHandler(func(_ *centrifuge.Client, ev centrifuge.ErrorEvent) {
		s.logger.Warn("donation alerts error", "err", ev.Message)
		if donationAlertsAuthError(ev.Message) {
			select {
			case failed <- fmt.Errorf("%w: %s", ErrExpired, ev.Message):
			default:
			}
		}
	}))
	client.OnDisconnect(daDisconnectHandler(func(_ *centrifuge.Client, ev centrifuge.DisconnectEvent) {
		if !ev.Reconnect {
			select {
			case disconnected <- ev.Reason:
			default:
			}
		}
	}))

	sub, err := client.NewSubscription(daDonationTopic + strconv.FormatInt(user.ID, 10))
	if err != nil {
		return fmt.Errorf("donation alerts subscription: %w", err)
	}
	sub.OnPublish(daPublishHandler(func(_ *centrifuge.Subscription, ev centrifuge.PublishEvent) {
		s.handlePublish(ev.Data, session)
	}))
	sub.OnSubscribeSuccess(daSubscribeSuccessHandler(func(*centrifuge.Subscription, centrifuge.SubscribeSuccessEvent) {
		select {
		case connected <- struct{}{}:
		default:
		}
		session.Connected()
	}))
	sub.OnSubscribeError(daSubscribeErrorHandler(func(_ *centrifuge.Subscription, ev centrifuge.SubscribeErrorEvent) {
		s.logger.Warn("donation alerts subscribe failed", "err", ev.Error)
		if donationAlertsAuthError(ev.Error) {
			select {
			case failed <- fmt.Errorf("%w: %s", ErrExpired, ev.Error):
			default:
			}
		}
	}))

	if err := sub.Subscribe(); err != nil {
		return fmt.Errorf("donation alerts subscribe: %w", err)
	}
	if err := client.Connect(); err != nil {
		return fmt.Errorf("donation alerts connect: %w", err)
	}
	defer client.Close()

	select {
	case <-ctx.Done():
		return nil
	case err := <-failed:
		return err
	case <-connected:
	case <-time.After(20 * time.Second):
		return fmt.Errorf("donation alerts connect timeout")
	}

	select {
	case <-ctx.Done():
		return nil
	case err := <-failed:
		return err
	case reason := <-disconnected:
		if donationAlertsAuthError(reason) {
			return fmt.Errorf("%w: %s", ErrExpired, reason)
		}
		return fmt.Errorf("donation alerts disconnected: %s", reason)
	}
}

func (s *donationalerts) handlePublish(data []byte, session Session) {
	s.logger.Info("donation event", "provider", db.ProviderDonationAlerts, "raw", string(data))

	event, ok, reason := parseDonationAlertsEvent(data)
	if !ok {
		s.logger.Info("donation event not queued", "provider", db.ProviderDonationAlerts, "reason", reason)
		return
	}
	session.Emit(event)
}

func (s *donationalerts) backfill(ctx context.Context, auth *donationAlertsAuth, since time.Time, session Session) error {
	var events []Event
	truncated := false

	for page := 1; page <= daBackfillMaxPage; page++ {
		body, err := auth.get(ctx, fmt.Sprintf("/api/v1/alerts/donations?page=%d", page))
		if err != nil {
			return err
		}

		var parsed struct {
			Data []json.RawMessage `json:"data"`
			Meta struct {
				CurrentPage int `json:"current_page"`
				LastPage    int `json:"last_page"`
			} `json:"meta"`
			Links struct {
				Next *string `json:"next"`
			} `json:"links"`
		}
		if err := json.Unmarshal(body, &parsed); err != nil {
			return fmt.Errorf("donation alerts history: %w", err)
		}
		if len(parsed.Data) == 0 {
			break
		}

		var oldest time.Time
		for _, raw := range parsed.Data {
			event, ok, reason := parseDonationAlertsEvent(raw)
			if !ok {
				if reason != "older than the watermark" {
					s.logger.Info("backfilled donation not queued", "reason", reason)
				}
				continue
			}
			if !event.CreatedAt.After(since) {
				continue
			}
			events = append(events, event)
			if oldest.IsZero() || event.CreatedAt.Before(oldest) {
				oldest = event.CreatedAt
			}
		}

		if parsed.Meta.LastPage == 0 || parsed.Meta.CurrentPage >= parsed.Meta.LastPage || parsed.Links.Next == nil {
			break
		}
		if !oldest.IsZero() && !oldest.After(since) {
			break
		}
		truncated = page == daBackfillMaxPage
	}

	if truncated {
		s.logger.Warn("donation alerts backfill truncated", "pages", daBackfillMaxPage)
	}
	sort.Slice(events, func(i, j int) bool { return events[i].CreatedAt.Before(events[j].CreatedAt) })
	for _, event := range events {
		session.Emit(event)
	}
	return nil
}

type daUser struct {
	ID                    int64  `json:"id"`
	Code                  string `json:"code"`
	Name                  string `json:"name"`
	SocketConnectionToken string `json:"socket_connection_token"`
}

type daUserResponse struct {
	Data daUser `json:"data"`
}

type donationAlertsAuth struct {
	source *donationalerts

	mu    sync.Mutex
	creds Credentials
}

func (a *donationAlertsAuth) user(ctx context.Context) (daUser, error) {
	body, err := a.get(ctx, "/api/v1/user/oauth")
	if err != nil {
		return daUser{}, err
	}
	var parsed daUserResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return daUser{}, fmt.Errorf("donation alerts user: %w", err)
	}
	return parsed.Data, nil
}

func (a *donationAlertsAuth) connectionToken(ctx context.Context) (string, error) {
	user, err := a.user(ctx)
	if err != nil {
		return "", err
	}
	if user.SocketConnectionToken == "" {
		return "", fmt.Errorf("%w: empty connection token", ErrAuth)
	}
	return user.SocketConnectionToken, nil
}

func (a *donationAlertsAuth) channelToken(ctx context.Context, clientID, channel string) (string, error) {
	payload, _ := json.Marshal(map[string]any{
		"client":   clientID,
		"channels": []string{channel},
	})
	body, err := a.do(ctx, http.MethodPost, "/api/v1/centrifuge/subscribe", payload)
	if err != nil {
		return "", err
	}

	var parsed struct {
		Channels []struct {
			Channel string `json:"channel"`
			Token   string `json:"token"`
		} `json:"channels"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("donation alerts channel token: %w", err)
	}
	for _, channel := range parsed.Channels {
		if channel.Token != "" {
			return channel.Token, nil
		}
	}
	return "", fmt.Errorf("%w: no channel token", ErrAuth)
}

func (a *donationAlertsAuth) get(ctx context.Context, path string) ([]byte, error) {
	return a.do(ctx, http.MethodGet, path, nil)
}

func (a *donationAlertsAuth) do(ctx context.Context, method, path string, payload []byte) ([]byte, error) {
	token, err := a.accessToken(ctx)
	if err != nil {
		return nil, err
	}

	body, status, err := a.request(ctx, method, path, payload, token)
	if err != nil {
		return nil, err
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		if err := a.refresh(ctx); err != nil {
			return nil, err
		}
		body, status, err = a.request(ctx, method, path, payload, a.tokenLocked())
		if err != nil {
			return nil, err
		}
		if status == http.StatusUnauthorized || status == http.StatusForbidden {
			return nil, fmt.Errorf("%w: status %d", ErrExpired, status)
		}
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("donation alerts %s: status %d", path, status)
	}
	return body, nil
}

func (a *donationAlertsAuth) request(ctx context.Context, method, path string, payload []byte, token string) ([]byte, int, error) {
	var reader io.Reader
	if payload != nil {
		reader = strings.NewReader(string(payload))
	}
	req, err := http.NewRequestWithContext(ctx, method, daAPIBase+path, reader)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := a.source.client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("donation alerts %s: %w", path, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return body, resp.StatusCode, nil
}

func (a *donationAlertsAuth) accessToken(ctx context.Context) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.creds.ExpiresAt != nil && time.Now().After(a.creds.ExpiresAt.Add(-time.Minute)) {
		return a.creds.AccessToken, a.refreshLocked(ctx)
	}
	return a.creds.AccessToken, nil
}

func (a *donationAlertsAuth) tokenLocked() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.creds.AccessToken
}

func (a *donationAlertsAuth) refresh(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.refreshLocked(ctx)
}

func (a *donationAlertsAuth) refreshLocked(ctx context.Context) error {
	if a.creds.RefreshToken == "" {
		return fmt.Errorf("%w: no refresh token", ErrExpired)
	}
	app := a.source.app
	if app.ClientID == "" || app.ClientSecret == "" {
		return fmt.Errorf("%w: no donation alerts app credentials", ErrExpired)
	}

	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", a.creds.RefreshToken)
	form.Set("client_id", app.ClientID)
	form.Set("client_secret", app.ClientSecret)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, daAPIBase+"/oauth/token", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := a.source.client.Do(req)
	if err != nil {
		return fmt.Errorf("donation alerts token refresh: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%w: token refresh status %d", ErrExpired, resp.StatusCode)
	}

	var parsed struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil || parsed.AccessToken == "" {
		return fmt.Errorf("%w: token refresh response", ErrExpired)
	}
	if parsed.RefreshToken == "" {
		parsed.RefreshToken = a.creds.RefreshToken
	}

	var expiresAt *time.Time
	if parsed.ExpiresIn > 0 {
		next := time.Now().UTC().Add(time.Duration(parsed.ExpiresIn) * time.Second)
		expiresAt = &next
	}

	if err := a.source.db.UpdateDonationProviderTokens(ctx, a.creds.UserID, db.ProviderDonationAlerts,
		parsed.AccessToken, parsed.RefreshToken, expiresAt); err != nil {
		a.source.logger.Error("failed to store refreshed donation alerts token", "user", a.creds.UserID, "err", err)
	}

	a.creds.AccessToken = parsed.AccessToken
	a.creds.RefreshToken = parsed.RefreshToken
	a.creds.ExpiresAt = expiresAt
	return nil
}

type daDonation struct {
	ID          int64   `json:"id"`
	Name        string  `json:"name"`
	Username    string  `json:"username"`
	MessageType string  `json:"message_type"`
	Message     string  `json:"message"`
	Amount      float64 `json:"amount"`
	Currency    string  `json:"currency"`
	CreatedAt   string  `json:"created_at"`
	Reason      string  `json:"reason"`
}

func parseDonationAlertsEvent(data []byte) (Event, bool, string) {
	var donation daDonation
	if err := json.Unmarshal(data, &donation); err != nil {
		return Event{}, false, "unreadable payload"
	}
	if name := strings.ToLower(strings.TrimSpace(donation.Name)); name != "" && name != "donation" && name != "donations" {
		return Event{}, false, "not a donation"
	}
	if donation.ID <= 0 {
		return Event{}, false, "no donation id"
	}

	currency := normalizeCurrency(donation.Currency)
	if len(currency) != 3 {
		return Event{}, false, "no currency"
	}
	amount := MinorUnits(donation.Amount, currency)
	if amount <= 0 {
		return Event{}, false, "no amount"
	}

	from := strings.TrimSpace(donation.Username)
	if from == "" {
		from = "anonymous"
	}

	createdAt := time.Now().UTC()
	for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02 15.04.05"} {
		if parsed, err := time.Parse(layout, donation.CreatedAt); err == nil {
			createdAt = parsed.UTC()
			break
		}
	}

	return Event{
		Provider:    db.ProviderDonationAlerts,
		ProviderID:  strconv.FormatInt(donation.ID, 10),
		From:        from,
		Message:     strings.TrimSpace(donation.Message),
		AmountMinor: amount,
		Currency:    currency,
		CreatedAt:   createdAt,
	}, true, ""
}

func donationAlertsAuthError(text string) bool {
	lower := strings.ToLower(text)
	for _, needle := range []string{"invalid token", "unauthorized", "expired", "forbidden"} {
		if strings.Contains(lower, needle) {
			return true
		}
	}
	return false
}

type daConnectHandler func(*centrifuge.Client, centrifuge.ConnectEvent)

func (f daConnectHandler) OnConnect(c *centrifuge.Client, e centrifuge.ConnectEvent) { f(c, e) }

type daDisconnectHandler func(*centrifuge.Client, centrifuge.DisconnectEvent)

func (f daDisconnectHandler) OnDisconnect(c *centrifuge.Client, e centrifuge.DisconnectEvent) {
	f(c, e)
}

type daErrorHandler func(*centrifuge.Client, centrifuge.ErrorEvent)

func (f daErrorHandler) OnError(c *centrifuge.Client, e centrifuge.ErrorEvent) { f(c, e) }

type daPrivateSubHandler func(*centrifuge.Client, centrifuge.PrivateSubEvent) (string, error)

func (f daPrivateSubHandler) OnPrivateSub(c *centrifuge.Client, e centrifuge.PrivateSubEvent) (string, error) {
	return f(c, e)
}

type daRefreshHandler func(*centrifuge.Client) (string, error)

func (f daRefreshHandler) OnRefresh(c *centrifuge.Client) (string, error) { return f(c) }

type daPublishHandler func(*centrifuge.Subscription, centrifuge.PublishEvent)

func (f daPublishHandler) OnPublish(s *centrifuge.Subscription, e centrifuge.PublishEvent) {
	f(s, e)
}

type daSubscribeSuccessHandler func(*centrifuge.Subscription, centrifuge.SubscribeSuccessEvent)

func (f daSubscribeSuccessHandler) OnSubscribeSuccess(s *centrifuge.Subscription, e centrifuge.SubscribeSuccessEvent) {
	f(s, e)
}

type daSubscribeErrorHandler func(*centrifuge.Subscription, centrifuge.SubscribeErrorEvent)

func (f daSubscribeErrorHandler) OnSubscribeError(s *centrifuge.Subscription, e centrifuge.SubscribeErrorEvent) {
	f(s, e)
}
