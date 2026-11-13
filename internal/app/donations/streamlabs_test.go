package donations

import (
	"encoding/json"
	"testing"
)

func TestParseStreamlabsDonation(t *testing.T) {
	raw := json.RawMessage(`{
		"type": "donation",
		"message": [{
			"id": 96164121,
			"name": "test",
			"from": "test",
			"amount": "13.37",
			"formatted_amount": "$13.37",
			"message": "test donation",
			"currency": "USD",
			"_id": "0820c9d5bafd768c9843f5e35c885e71"
		}],
		"event_id": "evt_17e5f4dc6888767ed9799f78dfa2cabc"
	}`)

	events, ok, reason := parseStreamlabsEvent(raw)
	if !ok {
		t.Fatalf("not parsed: %s", reason)
	}
	if len(events) != 1 {
		t.Fatalf("events: %d", len(events))
	}
	event := events[0]
	if event.ProviderID != "0820c9d5bafd768c9843f5e35c885e71" {
		t.Fatalf("provider id: %s", event.ProviderID)
	}
	if event.From != "test" {
		t.Fatalf("from: %s", event.From)
	}
	if event.Message != "test donation" {
		t.Fatalf("message: %s", event.Message)
	}
	if event.AmountMinor != 1337 || event.Currency != "USD" {
		t.Fatalf("amount: %d %s", event.AmountMinor, event.Currency)
	}
}

func TestParseStreamlabsNumericAmount(t *testing.T) {
	raw := json.RawMessage(`{
		"type": "donation",
		"message": [{"name": "Kevin", "amount": 97, "currency": "USD", "message": "hi", "_id": "abc"}],
		"event_id": "evt_1"
	}`)

	events, ok, reason := parseStreamlabsEvent(raw)
	if !ok || len(events) != 1 {
		t.Fatalf("not parsed: %s", reason)
	}
	if events[0].AmountMinor != 9700 {
		t.Fatalf("amount: %d", events[0].AmountMinor)
	}
}

func TestParseStreamlabsDropsTestAndOtherTypes(t *testing.T) {
	for name, raw := range map[string]string{
		"test":           `{"type":"donation","message":[{"name":"x","amount":10,"currency":"USD","isTest":true,"_id":"a"}]}`,
		"preview":        `{"type":"donation","message":[{"name":"x","amount":10,"currency":"USD","isPreview":true,"_id":"a"}]}`,
		"not donation":   `{"type":"subscription","message":[{"name":"x","amount":10,"currency":"USD","_id":"a"}]}`,
		"other platform": `{"type":"donation","for":"youtube","message":[{"name":"x","amount":10,"currency":"USD","_id":"a"}]}`,
	} {
		if _, ok, _ := parseStreamlabsEvent(json.RawMessage(raw)); ok {
			t.Errorf("%s was accepted", name)
		}
	}
}
