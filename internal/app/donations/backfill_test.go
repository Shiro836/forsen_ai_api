package donations

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"app/db"
)

func TestBackfillSince(t *testing.T) {
	if _, ok := backfillSince(Credentials{}); ok {
		t.Fatal("a credential with no last event must not backfill")
	}

	recent := time.Now().Add(-time.Hour)
	since, ok := backfillSince(Credentials{LastEventAt: &recent})
	if !ok || !since.Equal(recent) {
		t.Fatalf("since = %s, %v", since, ok)
	}

	old := time.Now().Add(-48 * time.Hour)
	since, ok = backfillSince(Credentials{LastEventAt: &old})
	if !ok || since.Before(time.Now().Add(-25*time.Hour)) {
		t.Fatalf("an old watermark must be capped at a day: %s, %v", since, ok)
	}
}

func TestStreamElementsBackfill(t *testing.T) {
	since := time.Date(2026, 11, 13, 19, 0, 0, 0, time.UTC)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			http.Error(w, "no token", http.StatusUnauthorized)
			return
		}
		if r.URL.Path != "/kappa/v2/tips/chan" {
			http.NotFound(w, r)
			return
		}

		page := r.URL.Query().Get("offset")
		var docs []string
		next := false
		switch page {
		case "0":
			docs = []string{
				`{"_id":"t1","tipId":"t1","createdAt":"2026-11-13T20:00:00Z","approved":"allowed","data":{"amount":4.2,"currency":"USD","displayName":"Backfill","message":"hello"}}`,
				`{"_id":"t0","tipId":"t0","createdAt":"2026-11-13T18:00:00Z","approved":"allowed","data":{"amount":1,"currency":"USD","username":"old","message":"old"}}`,
			}
			next = true
		case "100":
			docs = []string{
				`{"_id":"t2","tipId":"t2","createdAt":"2026-11-13T20:05:00Z","approved":"rejected","data":{"amount":2,"currency":"USD","username":"no","message":"no"}}`,
			}
		default:
			t.Fatalf("unexpected offset %q", page)
		}

		_ = json.NewEncoder(w).Encode(map[string]any{"docs": json.RawMessage("[" + strings.Join(docs, ",") + "]"), "hasNextPage": next})
	}))
	defer srv.Close()

	source := &streamElements{
		logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		http:    srv.Client(),
		apiBase: srv.URL,
	}

	var events []Event
	err := source.backfillTips(context.Background(), "chan", "test-token", since, Session{
		Emit: func(event Event) { events = append(events, event) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("%d events, want 1: %+v", len(events), events)
	}
	event := events[0]
	if event.Provider != db.ProviderStreamElements || event.ProviderID != "t1" {
		t.Fatalf("event: %+v", event)
	}
	if event.From != "Backfill" || event.Message != "hello" || event.AmountMinor != 420 {
		t.Fatalf("event: %+v", event)
	}
}

func TestParseStreamlabsHistory(t *testing.T) {
	since := time.Unix(1438576500, 0).UTC()
	created := json.RawMessage(`"1438576556"`)
	amount := json.RawMessage(`"50"`)
	message := "nice!"

	event, ok, reason := parseStreamlabsHistory(streamlabsDonationAPI{
		DonationID: json.RawMessage(`"80179029"`),
		CreatedAt:  created,
		Currency:   "USD",
		Amount:     amount,
		Name:       "Thomas",
		Message:    &message,
	}, since)
	if !ok {
		t.Fatalf("not parsed: %s", reason)
	}
	if event.ProviderID != "80179029" || event.From != "Thomas" || event.Message != "nice!" {
		t.Fatalf("event: %+v", event)
	}
	if event.AmountMinor != 5000 || event.Currency != "USD" {
		t.Fatalf("money: %+v", event)
	}

	if _, ok, _ := parseStreamlabsHistory(streamlabsDonationAPI{
		DonationID: json.RawMessage(`"1"`),
		CreatedAt:  json.RawMessage(`"1438576400"`),
		Currency:   "USD",
		Amount:     json.RawMessage(`"10"`),
	}, since); ok {
		t.Fatal("an event before the watermark was accepted")
	}
}
