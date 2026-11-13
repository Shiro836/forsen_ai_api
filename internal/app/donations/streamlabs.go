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
	"sort"
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
	streamlabsDonationsURL   = "https://streamlabs.com/api/v2.0/donations"
	slBackfillLimit          = 100
	slBackfillMaxPage        = 5
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

	if since, ok := backfillSince(creds); ok {
		s.logger.Info("backfilling donations", "since", since)
		if err := s.backfill(ctx, creds.AccessToken, since, session); err != nil {
			s.logger.Error("streamlabs backfill failed", "err", err)
		}
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

		// The numeric id is the one the REST history calls donation_id; using
		// it on both paths is what makes a backfilled event a no-op later.
		id := streamlabsFallbackID(donation.ID, "", 0)
		if id == "" {
			id = strings.TrimSpace(donation.DonationID)
		}
		if id == "" && payload.EventID != "" {
			id = payload.EventID + ":" + strconv.Itoa(i)
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

type streamlabsDonationAPI struct {
	DonationID json.RawMessage `json:"donation_id"`
	CreatedAt  json.RawMessage `json:"created_at"`
	Currency   string          `json:"currency"`
	Amount     json.RawMessage `json:"amount"`
	Name       string          `json:"name"`
	Message    *string         `json:"message"`
}

func (s *streamlabs) backfill(ctx context.Context, accessToken string, since time.Time, session Session) error {
	var events []Event
	truncated := false
	before := ""

	for page := 0; page < slBackfillMaxPage; page++ {
		q := url.Values{}
		q.Set("limit", strconv.Itoa(slBackfillLimit))
		q.Set("verified", "1")
		if before != "" {
			q.Set("before", before)
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, streamlabsDonationsURL+"?"+q.Encode(), nil)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+accessToken)
		req.Header.Set("Accept", "application/json")

		resp, err := s.client.Do(req)
		if err != nil {
			return fmt.Errorf("donations page %d: %w", page+1, err)
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
			resp.Body.Close()
			return fmt.Errorf("donations page %d: status %d", page+1, resp.StatusCode)
		}

		var body struct {
			Data []streamlabsDonationAPI `json:"data"`
		}
		err = json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&body)
		resp.Body.Close()
		if err != nil {
			return fmt.Errorf("donations page %d: %w", page+1, err)
		}
		if len(body.Data) == 0 {
			break
		}

		var oldest time.Time
		for _, donation := range body.Data {
			event, ok, reason := parseStreamlabsHistory(donation, since)
			if ok {
				events = append(events, event)
			} else if reason != "older than the watermark" {
				s.logger.Info("backfilled donation not queued", "reason", reason)
			}

			created := parseStreamlabsTime(donation.CreatedAt)
			if !created.IsZero() && (oldest.IsZero() || created.Before(oldest)) {
				oldest = created
			}
		}

		if len(body.Data) < slBackfillLimit {
			break
		}
		// The API returns newest first; once a page reaches the watermark,
		// every later page is older.
		if !oldest.IsZero() && !oldest.After(since) {
			break
		}
		before = streamlabsFallbackID(body.Data[len(body.Data)-1].DonationID, "", 0)
		if before == "" {
			break
		}
		truncated = page == slBackfillMaxPage-1
	}

	if truncated {
		s.logger.Warn("streamlabs backfill truncated", "pages", slBackfillMaxPage)
	}
	sort.Slice(events, func(i, j int) bool { return events[i].CreatedAt.Before(events[j].CreatedAt) })
	for _, event := range events {
		session.Emit(event)
	}
	return nil
}

func parseStreamlabsHistory(donation streamlabsDonationAPI, since time.Time) (Event, bool, string) {
	createdAt := parseStreamlabsTime(donation.CreatedAt)
	if createdAt.IsZero() {
		return Event{}, false, "no timestamp"
	}
	if !createdAt.After(since) {
		return Event{}, false, "older than the watermark"
	}

	id := streamlabsFallbackID(donation.DonationID, "", 0)
	if id == "" {
		return Event{}, false, "no donation id"
	}
	amount, ok := streamlabsAmount(donation.Amount)
	if !ok {
		return Event{}, false, "no amount"
	}
	currency := normalizeCurrency(donation.Currency)
	amountMinor := MinorUnits(amount, currency)
	if amountMinor <= 0 || len(currency) != 3 {
		return Event{}, false, "no amount"
	}

	from := strings.TrimSpace(donation.Name)
	if from == "" {
		from = "anonymous"
	}
	message := ""
	if donation.Message != nil {
		message = *donation.Message
	}

	return Event{
		Provider:    db.ProviderStreamlabs,
		ProviderID:  id,
		From:        from,
		Message:     strings.TrimSpace(message),
		AmountMinor: amountMinor,
		Currency:    currency,
		CreatedAt:   createdAt,
	}, true, ""
}

func parseStreamlabsTime(raw json.RawMessage) time.Time {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return time.Time{}
	}

	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		if seconds, err := strconv.ParseInt(strings.TrimSpace(text), 10, 64); err == nil {
			return time.Unix(seconds, 0).UTC()
		}
		return parseProviderTime(text)
	}

	var seconds int64
	if err := json.Unmarshal(raw, &seconds); err == nil {
		return time.Unix(seconds, 0).UTC()
	}
	return time.Time{}
}
