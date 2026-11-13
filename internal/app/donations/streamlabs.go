package donations

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"app/db"

	"github.com/zishang520/socket.io/clients/socket/v3"
	"github.com/zishang520/socket.io/v3/pkg/types"
)

const (
	streamlabsSocketEndpoint = "https://sockets.streamlabs.com"
	streamlabsTokenURL       = "https://streamlabs.com/api/v2.0/socket/token"
)

type streamlabs struct {
	logger *slog.Logger
	client *http.Client
}

func NewStreamlabs(logger *slog.Logger) Source {
	return &streamlabs{
		logger: logger,
		client: &http.Client{Timeout: 15 * time.Second},
	}
}

func (s *streamlabs) Provider() string { return db.ProviderStreamlabs }

func (s *streamlabs) Run(ctx context.Context, creds Credentials, session Session) error {
	socketToken, err := s.socketToken(ctx, creds.AccessToken)
	if err != nil {
		return err
	}

	opts := socket.DefaultOptions()
	opts.SetTransports(types.NewSet(socket.WebSocket))
	opts.SetTryAllTransports(false)
	opts.SetReconnection(false)

	conn, err := socket.Connect(streamlabsSocketEndpoint+"?token="+url.QueryEscape(socketToken), opts)
	if err != nil {
		return fmt.Errorf("streamlabs connect: %w", err)
	}
	defer conn.Close()

	var (
		connectOnce sync.Once
		failedOnce  sync.Once
	)
	connected := make(chan struct{})
	failed := make(chan error, 1)
	disconnected := make(chan string, 1)

	conn.On("connect", func(...any) {
		connectOnce.Do(func() { close(connected) })
		session.Connected()
	})
	conn.On("connect_error", func(args ...any) {
		err := streamlabsError("connect", args)
		failedOnce.Do(func() { failed <- err })
	})
	conn.On("disconnect", func(args ...any) {
		select {
		case disconnected <- fmt.Sprint(args...):
		default:
		}
	})
	conn.On("event", func(args ...any) {
		if len(args) == 0 {
			return
		}
		s.handleEvent(args[0], session)
	})

	select {
	case <-ctx.Done():
		return nil
	case err := <-failed:
		return err
	case <-connected:
	case <-time.After(20 * time.Second):
		return fmt.Errorf("streamlabs connect timeout")
	}

	select {
	case <-ctx.Done():
		return nil
	case err := <-failed:
		return err
	case reason := <-disconnected:
		return fmt.Errorf("streamlabs disconnected: %s", reason)
	}
}

func (s *streamlabs) socketToken(ctx context.Context, accessToken string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, streamlabsTokenURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")

	resp, err := s.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("streamlabs socket token: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return "", fmt.Errorf("%w: socket token status %d", ErrAuth, resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("streamlabs socket token: status %d", resp.StatusCode)
	}

	var parsed struct {
		SocketToken      string `json:"socket_token"`
		SocketTokenCamel string `json:"socketToken"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("streamlabs socket token: %w", err)
	}
	token := parsed.SocketToken
	if token == "" {
		token = parsed.SocketTokenCamel
	}
	if token == "" {
		return "", fmt.Errorf("streamlabs socket token: empty response")
	}
	return token, nil
}

func streamlabsError(where string, args []any) error {
	text := strings.TrimSpace(fmt.Sprint(args...))
	lower := strings.ToLower(text)
	switch {
	case strings.Contains(lower, "authentication"), strings.Contains(lower, "unauthorized"),
		strings.Contains(lower, "invalid token"), strings.Contains(lower, "forbidden"):
		return fmt.Errorf("%w: %s: %s", ErrAuth, where, text)
	default:
		return fmt.Errorf("streamlabs %s: %s", where, text)
	}
}

func (s *streamlabs) handleEvent(arg any, session Session) {
	raw, err := json.Marshal(arg)
	if err != nil {
		s.logger.Error("streamlabs event is not JSON", "err", err)
		return
	}
	s.logger.Info("donation event", "provider", db.ProviderStreamlabs, "raw", string(raw))

	events, ok, reason := parseStreamlabsEvent(raw)
	if !ok {
		s.logger.Info("donation event not queued", "provider", db.ProviderStreamlabs, "reason", reason)
		return
	}
	for _, event := range events {
		session.Emit(event)
	}
}

type streamlabsEvent struct {
	Type    string               `json:"type"`
	For     string               `json:"for"`
	EventID string               `json:"event_id"`
	Message []streamlabsDonation `json:"message"`
}

type streamlabsDonation struct {
	ID         json.RawMessage `json:"id"`
	Name       string          `json:"name"`
	From       string          `json:"from"`
	Amount     json.RawMessage `json:"amount"`
	Message    string          `json:"message"`
	Currency   string          `json:"currency"`
	IsTest     bool            `json:"isTest"`
	IsPreview  bool            `json:"isPreview"`
	DonationID string          `json:"_id"`
}

func parseStreamlabsEvent(raw json.RawMessage) ([]Event, bool, string) {
	var payload streamlabsEvent
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, false, "unreadable payload"
	}
	if payload.Type != "donation" {
		return nil, false, "not a donation"
	}
	if payload.For != "" && payload.For != "streamlabs" {
		return nil, false, "not this streamer"
	}

	var events []Event
	for i, donation := range payload.Message {
		if donation.IsTest || donation.IsPreview {
			continue
		}
		amount, ok := streamlabsAmount(donation.Amount)
		if !ok {
			continue
		}
		currency := normalizeCurrency(donation.Currency)
		amountMinor := MinorUnits(amount, currency)
		if amountMinor <= 0 || len(currency) != 3 {
			continue
		}

		id := strings.TrimSpace(donation.DonationID)
		if id == "" {
			id = streamlabsFallbackID(donation.ID, payload.EventID, i)
		}
		if id == "" {
			continue
		}

		from := strings.TrimSpace(donation.Name)
		if from == "" {
			from = strings.TrimSpace(donation.From)
		}
		if from == "" {
			from = "anonymous"
		}

		events = append(events, Event{
			Provider:    db.ProviderStreamlabs,
			ProviderID:  id,
			From:        from,
			Message:     strings.TrimSpace(donation.Message),
			AmountMinor: amountMinor,
			Currency:    currency,
			CreatedAt:   time.Now().UTC(),
		})
	}

	if len(events) == 0 {
		return nil, false, "no payable donation"
	}
	return events, true, ""
}

func streamlabsAmount(raw json.RawMessage) (float64, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return 0, false
	}

	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		amount, err := strconv.ParseFloat(strings.TrimSpace(text), 64)
		return amount, err == nil
	}

	var amount float64
	if err := json.Unmarshal(raw, &amount); err != nil {
		return 0, false
	}
	return amount, true
}

func streamlabsFallbackID(raw json.RawMessage, eventID string, index int) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) > 0 && !bytes.Equal(raw, []byte("null")) {
		var text string
		if err := json.Unmarshal(raw, &text); err == nil && text != "" {
			return text
		}
		var number json.Number
		if err := json.Unmarshal(raw, &number); err == nil && number != "" {
			return number.String()
		}
	}
	if eventID == "" {
		return ""
	}
	return eventID + ":" + strconv.Itoa(index)
}
