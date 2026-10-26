package delivery_test

import (
	"errors"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/delivery"
)

// Envelope's refusals are typed, so the outbox can name the member at
// fault: ErrNotForwarded for a new event, and a *DetailError for a Detail
// that is not one JSON object, carrying the syntax error when the text is
// not JSON at all.
func TestEnvelopeRefusalsAreTyped(t *testing.T) {
	forwarded := &delivery.Forwarded{ID: "e-1", Account: "111111111111", Region: "us-east-1", Time: time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)}
	if _, err := (delivery.EventEntry{Source: "s", DetailType: "t", Detail: `{}`}).Envelope(); !errors.Is(err, delivery.ErrNotForwarded) {
		t.Errorf("Envelope of a new event = %v, want ErrNotForwarded", err)
	}
	for detail, syntax := range map[string]bool{`["a"]`: false, `"a"`: false, `{"a":`: true, ``: true} {
		_, err := (delivery.EventEntry{Source: "s", DetailType: "t", Detail: detail, Forwarded: forwarded}).Envelope()
		de, ok := errors.AsType[*delivery.DetailError](err)
		if !ok || de.Detail != detail || (de.Err != nil) != syntax {
			t.Errorf("Envelope with Detail %q = %v, want a *DetailError (syntax error %t)", detail, err, syntax)
		}
	}
}
