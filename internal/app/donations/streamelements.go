package donations

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"app/db"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

const (
	astroEndpoint    = "wss://astro.streamelements.com/"
	astroTopic       = "channel.activities"
	astroReadTimeout = 90 * time.Second
)

// streamElements is the Astro websocket client. The protocol is StreamElements'
// documented gateway; there is no Go client to depend on, so the frame layer is
// gorilla and everything above it is the documented subscribe/publish shape.
type streamElements struct {
	logger *slog.Logger
	dialer *websocket.Dialer
}

func NewStreamElements(logger *slog.Logger) Source {
	return &streamElements{
		logger: logger,
		dialer: &websocket.Dialer{HandshakeTimeout: 10 * time.Second},
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

	return s.serve(ctx, channel, creds.AccessToken, session)
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

func (s *streamElements) serve(ctx context.Context, channel, token string, session Session) error {
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

			event, ok, reason := parseStreamElementsActivity(msg.Data)
			if !ok {
				s.logger.Info("donation event not queued", "provider", db.ProviderStreamElements, "reason", reason)
				continue
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

func parseStreamElementsActivity(raw json.RawMessage) (Event, bool, string) {
	var activity streamElementsActivity
	if err := json.Unmarshal(raw, &activity); err != nil {
		return Event{}, false, "unreadable payload"
	}
	if activity.Type != "tip" {
		return Event{}, false, "not a tip"
	}

	id := activity.ActivityID
	if id == "" {
		id = activity.ID
	}
	if id == "" {
		return Event{}, false, "no activity id"
	}

	currency := normalizeCurrency(activity.Data.Currency)
	if len(currency) != 3 {
		return Event{}, false, "no currency"
	}
	amount := MinorUnits(activity.Data.Amount, currency)
	if amount <= 0 {
		return Event{}, false, "no amount"
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
	}, true, ""
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
