package donations

import (
	"bytes"
	"context"
	"encoding/base64"
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

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

const (
	astroEndpoint     = "wss://astro.streamelements.com/"
	astroTopic        = "channel.activities"
	astroReadTimeout  = 90 * time.Second
	seAPIBase         = "https://api.streamelements.com"
	seBackfillLimit   = 100
	seBackfillMaxPage = 5
)

// streamElements is the Astro websocket client. The protocol is StreamElements'
// documented gateway; there is no Go client to depend on, so the frame layer is
// gorilla and everything above it is the documented subscribe/publish shape.
type streamElements struct {
	logger  *slog.Logger
	dialer  *websocket.Dialer
	http    *http.Client
	apiBase string
}

func NewStreamElements(logger *slog.Logger) Source {
	return &streamElements{
		logger:  logger,
		dialer:  &websocket.Dialer{HandshakeTimeout: 10 * time.Second},
		http:    &http.Client{Timeout: 20 * time.Second},
		apiBase: seAPIBase,
	}
}

func (s *streamElements) Provider() string { return db.ProviderStreamElements }

func (s *streamElements) Run(ctx context.Context, creds Credentials, session Session) error {
	if exp, ok := jwtExpiry(creds.AccessToken); ok && time.Now().After(exp) {
		return ErrExpired
	}
	channel, err := jwtClaim(creds.AccessToken, "channel")
	if err != nil || channel == "" {
		return fmt.Errorf("%w: %v", ErrAuth, err)
	}

	currency, err := s.tippingCurrency(ctx, channel, creds.AccessToken)
	if err != nil {
		s.logger.Warn("streamelements channel currency lookup failed", "err", err)
	}

	if since, ok := backfillSince(creds); ok {
		s.logger.Info("backfilling tips", "since", since)
		if err := s.backfillTips(ctx, channel, creds.AccessToken, since, session); err != nil {
			// A REST outage must not keep the live socket down; the next
			// reconnect retries from the same watermark.
			s.logger.Error("streamelements backfill failed", "err", err)
		}
	}

	return s.serve(ctx, channel, creds.AccessToken, currency, session)
}

// tippingCurrency is the channel's configured tip currency. Socket activities
// can omit currency, and that must not turn a real or mock tip into a drop.
func (s *streamElements) tippingCurrency(ctx context.Context, channel, token string) (string, error) {
	endpoint := fmt.Sprintf("%s/kappa/v2/tipping/%s", s.apiBase, url.PathEscape(channel))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	resp, err := s.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("tipping settings: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		return "", fmt.Errorf("tipping settings: status %d", resp.StatusCode)
	}

	var body struct {
		Currency struct {
			Code string `json:"code"`
		} `json:"currency"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return "", fmt.Errorf("tipping settings: %w", err)
	}

	currency := normalizeCurrency(body.Currency.Code)
	if len(currency) != 3 {
		return "", fmt.Errorf("tipping settings: no currency")
	}
	return currency, nil
}

type astroMessage struct {
	ID    string          `json:"id"`
	Type  string          `json:"type"`
	Topic string          `json:"topic"`
	Room  string          `json:"room"`
	Nonce string          `json:"nonce"`
	Error string          `json:"error"`
	Data  json.RawMessage `json:"data"`
}

func (s *streamElements) serve(ctx context.Context, channel, token, fallbackCurrency string, session Session) error {
	conn, err := s.dial(ctx, astroEndpoint)
	if err != nil {
		return err
	}

	var (
		connMu  sync.Mutex
		current = conn
	)
	defer func() {
		connMu.Lock()
		_ = current.Close()
		connMu.Unlock()
	}()
	setConn := func(next *websocket.Conn) {
		connMu.Lock()
		current = next
		connMu.Unlock()
	}
	closed := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			connMu.Lock()
			_ = current.Close()
			connMu.Unlock()
		case <-closed:
		}
	}()
	defer close(closed)

	s.readDeadline(conn)

	subscribed := false
	connected := false
	markConnected := func() {
		if !connected {
			connected = true
			session.Connected()
		}
	}

	for {
		var msg astroMessage
		if err := current.ReadJSON(&msg); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		s.readDeadline(current)

		switch msg.Type {
		case "welcome":
			if !subscribed {
				if err := s.subscribe(current, channel, token); err != nil {
					return err
				}
				subscribed = true
			} else {
				// A reconnect token restored this connection's subscriptions.
				markConnected()
			}
		case "response":
			if msg.Error != "" {
				switch msg.Error {
				case "err_unauthorized", "err_forbidden":
					return fmt.Errorf("%w: %s", ErrAuth, msg.Error)
				case "rate_limit_exceeded":
					return fmt.Errorf("astro: rate limit exceeded")
				default:
					return fmt.Errorf("astro subscribe: %s", msg.Error)
				}
			}
			markConnected()
		case "message":
			if msg.Topic != astroTopic {
				continue
			}
			s.logger.Info("donation event", "provider", db.ProviderStreamElements, "raw", string(msg.Data))

			event, ok, reason, usedFallback := parseStreamElementsActivity(msg.Data, fallbackCurrency)
			if !ok {
				s.logger.Info("donation event not queued", "provider", db.ProviderStreamElements, "reason", reason)
				continue
			}
			if usedFallback {
				s.logger.Warn("streamelements activity had no currency, using the channel's", "currency", event.Currency)
			}
			markConnected()
			session.Emit(event)
		case "reconnect":
			var data struct {
				ReconnectToken string `json:"reconnect_token"`
			}
			if err := json.Unmarshal(msg.Data, &data); err != nil || data.ReconnectToken == "" {
				return fmt.Errorf("astro reconnect: missing token")
			}
			next, err := s.dial(ctx, astroReconnectURL(data.ReconnectToken))
			if err != nil {
				return err
			}
			_ = current.Close()
			setConn(next)
			s.readDeadline(next)
			subscribed = true
		}
	}
}

func (s *streamElements) dial(ctx context.Context, endpoint string) (*websocket.Conn, error) {
	conn, resp, err := s.dialer.DialContext(ctx, endpoint, nil)
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusTooManyRequests {
			return nil, fmt.Errorf("astro: rate limited, retry after %s", resp.Header.Get("Retry-After"))
		}
		return nil, fmt.Errorf("astro dial: %w", err)
	}
	return conn, nil
}

func (s *streamElements) subscribe(conn *websocket.Conn, channel, token string) error {
	msg := map[string]any{
		"type":  "subscribe",
		"nonce": uuid.NewString(),
		"data": map[string]any{
			"topic":      astroTopic,
			"room":       channel,
			"token":      token,
			"token_type": "jwt",
		},
	}
	if err := conn.WriteJSON(msg); err != nil {
		return fmt.Errorf("astro subscribe: %w", err)
	}
	return nil
}

func (s *streamElements) readDeadline(conn *websocket.Conn) {
	_ = conn.SetReadDeadline(time.Now().Add(astroReadTimeout))
	conn.SetPingHandler(func(appData string) error {
		_ = conn.SetReadDeadline(time.Now().Add(astroReadTimeout))
		return conn.WriteControl(websocket.PongMessage, []byte(appData), time.Now().Add(5*time.Second))
	})
}

func astroReconnectURL(token string) string {
	q := url.Values{"reconnect_token": {token}}
	return astroEndpoint + "?" + q.Encode()
}

type streamElementsActivity struct {
	ID         string `json:"_id"`
	ActivityID string `json:"activityId"`
	Channel    string `json:"channel"`
	Data       struct {
		Amount      float64 `json:"amount"`
		Currency    string  `json:"currency"`
		Username    string  `json:"username"`
		DisplayName string  `json:"displayName"`
		Message     string  `json:"message"`
		ProviderID  string  `json:"providerId"`
	} `json:"data"`
	IsMock    bool   `json:"isMock"`
	Provider  string `json:"provider"`
	Type      string `json:"type"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
}

func parseStreamElementsActivity(raw json.RawMessage, fallbackCurrency string) (Event, bool, string, bool) {
	var activity streamElementsActivity
	if err := json.Unmarshal(raw, &activity); err != nil {
		return Event{}, false, "unreadable payload", false
	}
	if activity.Type != "tip" {
		return Event{}, false, "not a tip", false
	}

	id := activity.ActivityID
	if id == "" {
		id = activity.ID
	}
	if id == "" {
		return Event{}, false, "no activity id", false
	}

	currency := normalizeCurrency(activity.Data.Currency)
	usedFallback := false
	if len(currency) != 3 {
		currency = normalizeCurrency(fallbackCurrency)
		usedFallback = len(currency) == 3
	}
	if len(currency) != 3 {
		return Event{}, false, "no currency", false
	}
	amount := MinorUnits(activity.Data.Amount, currency)
	if amount <= 0 {
		return Event{}, false, "no amount", false
	}

	from := strings.TrimSpace(activity.Data.DisplayName)
	if from == "" {
		from = strings.TrimSpace(activity.Data.Username)
	}
	if from == "" {
		from = "anonymous"
	}

	createdAt := parseProviderTime(activity.CreatedAt)
	if createdAt.IsZero() {
		createdAt = parseProviderTime(activity.UpdatedAt)
	}
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}

	return Event{
		Provider:    db.ProviderStreamElements,
		ProviderID:  id,
		From:        from,
		Message:     strings.TrimSpace(activity.Data.Message),
		AmountMinor: amount,
		Currency:    currency,
		CreatedAt:   createdAt,
		Mock:        activity.IsMock,
	}, true, "", usedFallback
}

func parseProviderTime(value string) time.Time {
	if value == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return t
	}
	return time.Time{}
}

type streamElementsTip struct {
	ID        string          `json:"_id"`
	TipID     string          `json:"tipId"`
	CreatedAt json.RawMessage `json:"createdAt"`
	Deleted   bool            `json:"deleted"`
	Approved  string          `json:"approved"`
	Data      struct {
		Amount      float64 `json:"amount"`
		Currency    string  `json:"currency"`
		Username    string  `json:"username"`
		DisplayName string  `json:"displayName"`
		Message     string  `json:"message"`
	} `json:"data"`
}

func (s *streamElements) backfillTips(ctx context.Context, channel, token string, since time.Time, session Session) error {
	var events []Event
	truncated := false

	for page := 0; page < seBackfillMaxPage; page++ {
		q := url.Values{}
		q.Set("limit", strconv.Itoa(seBackfillLimit))
		q.Set("offset", strconv.Itoa(page*seBackfillLimit))
		endpoint := fmt.Sprintf("%s/kappa/v2/tips/%s?%s", s.apiBase, url.PathEscape(channel), q.Encode())

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", "application/json")

		resp, err := s.http.Do(req)
		if err != nil {
			return fmt.Errorf("tips page %d: %w", page+1, err)
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
			resp.Body.Close()
			return fmt.Errorf("tips page %d: status %d", page+1, resp.StatusCode)
		}

		var body struct {
			Docs        []json.RawMessage `json:"docs"`
			HasNextPage bool              `json:"hasNextPage"`
		}
		err = json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&body)
		resp.Body.Close()
		if err != nil {
			return fmt.Errorf("tips page %d: %w", page+1, err)
		}

		for _, raw := range body.Docs {
			event, ok, reason := parseStreamElementsTip(raw, since)
			if !ok {
				if reason != "older than the watermark" {
					s.logger.Info("backfilled tip not queued", "reason", reason)
				}
				continue
			}
			events = append(events, event)
		}

		if !body.HasNextPage || len(body.Docs) == 0 {
			break
		}
		truncated = page == seBackfillMaxPage-1
	}

	if truncated {
		s.logger.Warn("streamelements backfill truncated", "pages", seBackfillMaxPage)
	}
	sort.Slice(events, func(i, j int) bool { return events[i].CreatedAt.Before(events[j].CreatedAt) })
	for _, event := range events {
		session.Emit(event)
	}
	return nil
}

func parseStreamElementsTip(raw json.RawMessage, since time.Time) (Event, bool, string) {
	var tip streamElementsTip
	if err := json.Unmarshal(raw, &tip); err != nil {
		return Event{}, false, "unreadable payload"
	}
	if tip.Deleted {
		return Event{}, false, "deleted"
	}
	if strings.EqualFold(tip.Approved, "rejected") {
		return Event{}, false, "rejected"
	}

	id := tip.TipID
	if id == "" {
		id = tip.ID
	}
	if id == "" {
		return Event{}, false, "no tip id"
	}

	createdAt := parseTipTime(tip.CreatedAt)
	if createdAt.IsZero() {
		return Event{}, false, "no timestamp"
	}
	if !createdAt.After(since) {
		return Event{}, false, "older than the watermark"
	}

	currency := normalizeCurrency(tip.Data.Currency)
	if len(currency) != 3 {
		return Event{}, false, "no currency"
	}
	amount := MinorUnits(tip.Data.Amount, currency)
	if amount <= 0 {
		return Event{}, false, "no amount"
	}

	from := strings.TrimSpace(tip.Data.DisplayName)
	if from == "" {
		from = strings.TrimSpace(tip.Data.Username)
	}
	if from == "" {
		from = "anonymous"
	}

	return Event{
		Provider:    db.ProviderStreamElements,
		ProviderID:  id,
		From:        from,
		Message:     strings.TrimSpace(tip.Data.Message),
		AmountMinor: amount,
		Currency:    currency,
		CreatedAt:   createdAt,
	}, true, ""
}

func parseTipTime(raw json.RawMessage) time.Time {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return time.Time{}
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		if parsed := parseProviderTime(text); !parsed.IsZero() {
			return parsed
		}
		if seconds, err := strconv.ParseInt(text, 10, 64); err == nil {
			return time.Unix(seconds, 0).UTC()
		}
	}
	var seconds int64
	if err := json.Unmarshal(raw, &seconds); err == nil {
		return time.Unix(seconds, 0).UTC()
	}
	return time.Time{}
}

// ParseStreamElementsToken validates a pasted dashboard JWT. The connect path
// needs the channel claim, so a token without one is not usable here.
func ParseStreamElementsToken(token string) (expiresAt time.Time, valid bool) {
	if channel, err := jwtClaim(token, "channel"); err != nil || channel == "" {
		return time.Time{}, false
	}
	if exp, ok := jwtExpiry(token); ok {
		return exp, true
	}
	return time.Time{}, true
}

func jwtPayload(token string) (map[string]any, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("not a JWT")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		raw, err = base64.URLEncoding.DecodeString(parts[1])
		if err != nil {
			return nil, err
		}
	}
	var claims map[string]any
	if err := json.Unmarshal(raw, &claims); err != nil {
		return nil, err
	}
	return claims, nil
}

func jwtClaim(token, name string) (string, error) {
	claims, err := jwtPayload(token)
	if err != nil {
		return "", err
	}
	switch value := claims[name].(type) {
	case string:
		return value, nil
	case float64:
		return fmt.Sprintf("%.0f", value), nil
	default:
		return "", fmt.Errorf("jwt has no %s claim", name)
	}
}

func jwtExpiry(token string) (time.Time, bool) {
	claims, err := jwtPayload(token)
	if err != nil {
		return time.Time{}, false
	}
	exp, ok := claims["exp"].(float64)
	if !ok || exp <= 0 {
		return time.Time{}, false
	}
	return time.Unix(int64(exp), 0), true
}
