package donations

import (
	"testing"
	"time"
)

func TestParseDonationAlertsEvent(t *testing.T) {
	event, ok, reason := parseDonationAlertsEvent([]byte(`{
		"id": 30530030,
		"name": "donation",
		"username": "Ivan",
		"message_type": "text",
		"message": "Hello!",
		"amount": 500,
		"currency": "RUB",
		"is_shown": 1,
		"created_at": "2019-09-29 09:00:00",
		"reason": "donation"
	}`))
	if !ok {
		t.Fatalf("not parsed: %s", reason)
	}
	if event.ProviderID != "30530030" {
		t.Fatalf("provider id: %s", event.ProviderID)
	}
	if event.From != "Ivan" || event.Message != "Hello!" {
		t.Fatalf("from/message: %q / %q", event.From, event.Message)
	}
	if event.AmountMinor != 50000 || event.Currency != "RUB" {
		t.Fatalf("amount: %d %s", event.AmountMinor, event.Currency)
	}
	want := time.Date(2019, 9, 29, 9, 0, 0, 0, time.UTC)
	if !event.CreatedAt.Equal(want) {
		t.Fatalf("created at: %s", event.CreatedAt)
	}
}

func TestParseDonationAlertsRejectsBadEvents(t *testing.T) {
	for name, raw := range map[string]string{
		"not a donation": `{"id":1,"name":"goal","amount":10,"currency":"USD"}`,
		"no id":          `{"name":"donation","amount":10,"currency":"USD"}`,
		"no amount":      `{"id":1,"name":"donation","amount":0,"currency":"USD"}`,
		"no currency":    `{"id":1,"name":"donation","amount":10}`,
	} {
		if _, ok, _ := parseDonationAlertsEvent([]byte(raw)); ok {
			t.Errorf("%s was accepted", name)
		}
	}
}
