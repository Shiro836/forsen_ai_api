package donations

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

const capturedActivity = `{
  "_id": "6af76d10badb9966003eb6d0",
  "activityId": "6af76d10badb9966003eb6d0",
  "channel": "611e07bf7deeb4288b2b6a4c",
  "data": {
    "amount": 4.2,
    "avatar": "",
    "currency": "USD",
    "displayName": "AstroProbe",
    "message": "astro probe",
    "providerId": "82054454",
    "username": "astroprobe"
  },
  "isMock": true,
  "provider": "twitch",
  "sessionEventsCount": 1,
  "type": "tip",
  "updatedAt": "2026-11-13T20:03:28.453Z"
}`

func TestParseStreamElementsActivity(t *testing.T) {
	event, ok, reason, usedFallback := parseStreamElementsActivity(json.RawMessage(capturedActivity), "EUR")
	if !ok {
		t.Fatalf("not parsed: %s", reason)
	}
	if usedFallback {
		t.Fatal("an activity that names its currency used the fallback")
	}
	if event.ProviderID != "6af76d10badb9966003eb6d0" {
		t.Fatalf("provider id: %s", event.ProviderID)
	}
	if event.From != "AstroProbe" {
		t.Fatalf("from: %s", event.From)
	}
	if event.Message != "astro probe" {
		t.Fatalf("message: %s", event.Message)
	}
	if event.AmountMinor != 420 || event.Currency != "USD" {
		t.Fatalf("amount: %d %s", event.AmountMinor, event.Currency)
	}
	if !event.Mock {
		t.Fatal("mock flag lost")
	}
	want := time.Date(2026, 11, 13, 20, 3, 28, 453000000, time.UTC)
	if !event.CreatedAt.Equal(want) {
		t.Fatalf("created at: %s", event.CreatedAt)
	}
}

func TestParseStreamElementsActivityRejectsNonTips(t *testing.T) {
	for name, raw := range map[string]string{
		"not a tip":      `{"activityId":"1","type":"follow","data":{"amount":4.2,"currency":"USD","message":"x"}}`,
		"no amount":      `{"activityId":"1","type":"tip","data":{"amount":0,"currency":"USD","message":"x"}}`,
		"no currency":    `{"activityId":"1","type":"tip","data":{"amount":4.2,"message":"x"}}`,
		"no activity id": `{"type":"tip","data":{"amount":4.2,"currency":"USD","message":"x"}}`,
	} {
		if _, ok, _, _ := parseStreamElementsActivity(json.RawMessage(raw), ""); ok {
			t.Errorf("%s was accepted", name)
		}
	}
}

// The mock endpoint drops the currency the dashboard sends, so the channel's
// tipping currency is the only one a mock tip can be in.
func TestParseStreamElementsActivityFallsBackToChannelCurrency(t *testing.T) {
	raw := json.RawMessage(`{
	  "_id": "6af7845e5c0245903fb33aee",
	  "activityId": "6af7845e5c0245903fb33aee",
	  "channel": "611e07bf7deeb4288b2b6a4c",
	  "data": {
	    "amount": 10,
	    "username": "simmonds",
	    "displayName": "simmonds",
	    "message": "Howdy, my name is Bill"
	  },
	  "isMock": true,
	  "provider": "twitch",
	  "type": "tip",
	  "createdAt": "2026-11-13T21:42:54.368Z"
	}`)

	event, ok, reason, usedFallback := parseStreamElementsActivity(raw, "usd")
	if !ok {
		t.Fatalf("not parsed: %s", reason)
	}
	if !usedFallback {
		t.Fatal("a currency-less activity did not use the fallback")
	}
	if event.AmountMinor != 1000 || event.Currency != "USD" {
		t.Fatalf("amount: %d %s", event.AmountMinor, event.Currency)
	}
	if !event.Mock {
		t.Fatal("mock flag lost")
	}
}

func TestTippingCurrency(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/kappa/v2/tipping/611e07bf7deeb4288b2b6a4c" {
			t.Errorf("path: %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer tok" {
			t.Errorf("authorization: %q", got)
		}
		_, _ = w.Write([]byte(`{"currency":{"code":"eur","name":"Euro","symbol":"€"}}`))
	}))
	defer srv.Close()

	source := &streamElements{http: srv.Client(), apiBase: srv.URL}
	currency, err := source.tippingCurrency(context.Background(), "611e07bf7deeb4288b2b6a4c", "tok")
	if err != nil {
		t.Fatal(err)
	}
	if currency != "EUR" {
		t.Fatalf("currency: %q", currency)
	}
}

func TestTippingCurrencyWithoutCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"currency":{}}`))
	}))
	defer srv.Close()

	source := &streamElements{http: srv.Client(), apiBase: srv.URL}
	if _, err := source.tippingCurrency(context.Background(), "chan", "tok"); err == nil {
		t.Fatal("a channel without a currency was accepted")
	}
}

func TestJWTClaims(t *testing.T) {
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"channel":"611e07bf7deeb4288b2b6a4c","exp":1893456000}`))
	token := "eyJhbGciOiJIUzI1NiJ9." + payload + ".signature"

	channel, err := jwtClaim(token, "channel")
	if err != nil || channel != "611e07bf7deeb4288b2b6a4c" {
		t.Fatalf("channel: %q, %v", channel, err)
	}
	exp, ok := jwtExpiry(token)
	if !ok || !exp.Equal(time.Unix(1893456000, 0)) {
		t.Fatalf("expiry: %s, %v", exp, ok)
	}
}
