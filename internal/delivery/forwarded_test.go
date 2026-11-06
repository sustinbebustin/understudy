package delivery_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/delivery"
)

// envelopeKeys is the member order of a delivered event, as AWS recorded it
// (secrets-logs-events-alarms.md 4.3, "key order as recorded").
var envelopeKeys = []string{"version", "id", "detail-type", "source", "account", "time", "region", "resources", "detail"}

// objectKeys returns the top-level member names of the JSON object data, in
// the order they appear.
func objectKeys(t interface{ Fatalf(string, ...any) }, data []byte) []string {
	dec := json.NewDecoder(bytes.NewReader(data))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		t.Fatalf("%s is not a JSON object: %v %v", data, tok, err)
	}
	var keys []string
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			t.Fatalf("%s: %v", data, err)
		}
		key, ok := tok.(string)
		if !ok {
			t.Fatalf("%s: member name %v", data, tok)
		}
		keys = append(keys, key)
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			t.Fatalf("%s: member %s: %v", data, key, err)
		}
	}
	if _, err := dec.Token(); err != nil {
		t.Fatalf("%s: %v", data, err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		t.Fatalf("%s: trailing data after the object", data)
	}
	return keys
}

// A forwarded event's envelope is the event as the destination bus receives
// it: the recorded member order, the forwarded id, account, region and time,
// and the entry's source, detail-type, resources and detail
// (eventbridge-buses.md 6.2, 10.4.12; the time is fractionless UTC,
// secrets-logs-events-alarms.md 4.3).
func TestEnvelopeIsTheForwardedEvent(t *testing.T) {
	at := time.Date(2026, 10, 14, 7, 14, 15, 0, time.FixedZone("x", 2*60*60))
	e := delivery.EventEntry{
		Source: "shop", DetailType: "order", Detail: `{ "total" : 250, "items": [1, 2] }`,
		Resources: []string{"arn:aws:s3:::bucket"},
		Forwarded: &delivery.Forwarded{ID: "6a7e8feb-b491-4cf7-a9f1-bf3703467718", Account: "111111111111", Region: "us-east-1", Time: at},
	}
	got, err := e.Envelope()
	if err != nil {
		t.Fatalf("Envelope = %v", err)
	}
	want := `{"version":"0","id":"6a7e8feb-b491-4cf7-a9f1-bf3703467718","detail-type":"order","source":"shop",` +
		`"account":"111111111111","time":"2026-10-14T05:14:15Z","region":"us-east-1","resources":["arn:aws:s3:::bucket"],` +
		`"detail":{"total":250,"items":[1,2]}}`
	if string(got) != want {
		t.Errorf("Envelope =\n%s\nwant\n%s", got, want)
	}

	// An event put without resources has "resources": [], not null or
	// absent (secrets-logs-events-alarms.md 4.7, aws-recorded SQS bodies).
	e.Resources = nil
	got, err = e.Envelope()
	if err != nil {
		t.Fatalf("Envelope without resources = %v", err)
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(got, &decoded); err != nil {
		t.Fatalf("Envelope without resources = %s: %v", got, err)
	}
	if string(decoded["resources"]) != "[]" {
		t.Errorf("resources = %s, want []", decoded["resources"])
	}
}

// An entry with no Forwarded is a new event, which has no id yet, and a
// Detail that is not a JSON object is not an event (MalformedDetail,
// secrets-logs-events-alarms.md 4.7): neither has an envelope.
func TestEnvelopeRefusesAnEntryThatIsNoForwardedEvent(t *testing.T) {
	forwarded := &delivery.Forwarded{ID: "e-1", Account: "111111111111", Region: "us-east-1", Time: time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)}
	for name, e := range map[string]delivery.EventEntry{
		"no Forwarded":       {Source: "s", DetailType: "t", Detail: `{}`},
		"an array Detail":    {Source: "s", DetailType: "t", Detail: `["a"]`, Forwarded: forwarded},
		"a string Detail":    {Source: "s", DetailType: "t", Detail: `"a"`, Forwarded: forwarded},
		"a truncated Detail": {Source: "s", DetailType: "t", Detail: `{"a":`, Forwarded: forwarded},
	} {
		if got, err := e.Envelope(); err == nil {
			t.Errorf("%s: Envelope = %s, want an error", name, got)
		}
	}
}

// Every envelope is one compact JSON object in the recorded member order
// whose members carry the entry and its Forwarded unchanged, the detail
// compacted, whatever the entry holds.
func TestEnvelopeProperty(t *testing.T) {
	text := rapid.StringMatching(`[A-Za-z0-9 ."\\/:é-]{0,16}`)
	detail := rapid.SampledFrom([]string{`{}`, `{"a":1}`, "{ \"a\" : [ 1 , {\"b\" : null} ] }", `{"nested":{"deep":{"x":"y z"}}}`, "{\n\t\"n\": -1.5e3\n}"})
	rapid.Check(t, func(t *rapid.T) {
		at := time.Unix(rapid.Int64Range(0, 4e9).Draw(t, "unix"), 0).UTC()
		e := delivery.EventEntry{
			Source: text.Draw(t, "source"), DetailType: text.Draw(t, "detailType"), Detail: detail.Draw(t, "detail"),
			Resources: rapid.SliceOfN(text, 0, 3).Draw(t, "resources"),
			Forwarded: &delivery.Forwarded{
				ID:      rapid.StringMatching(`[0-9a-f-]{1,36}`).Draw(t, "id"),
				Account: rapid.StringMatching(`[0-9]{12}`).Draw(t, "account"),
				Region:  rapid.SampledFrom([]string{"us-east-1", "eu-west-1", "ap-southeast-2"}).Draw(t, "region"),
				Time:    at,
			},
		}
		got, err := e.Envelope()
		if err != nil {
			t.Fatalf("Envelope(%+v) = %v", e, err)
		}
		if keys := objectKeys(t, got); !slices.Equal(keys, envelopeKeys) {
			t.Fatalf("members %v, want %v", keys, envelopeKeys)
		}
		var compact bytes.Buffer
		if err := json.Compact(&compact, got); err != nil || !bytes.Equal(compact.Bytes(), got) {
			t.Fatalf("Envelope = %s, not compact JSON (%v)", got, err)
		}
		var ev struct {
			Version    string          `json:"version"`
			ID         string          `json:"id"`
			DetailType string          `json:"detail-type"`
			Source     string          `json:"source"`
			Account    string          `json:"account"`
			Time       string          `json:"time"`
			Region     string          `json:"region"`
			Resources  []string        `json:"resources"`
			Detail     json.RawMessage `json:"detail"`
		}
		if err := json.Unmarshal(got, &ev); err != nil {
			t.Fatalf("Envelope = %s: %v", got, err)
		}
		var wantDetail bytes.Buffer
		if err := json.Compact(&wantDetail, []byte(e.Detail)); err != nil {
			t.Fatal(err)
		}
		wantResources := e.Resources
		if wantResources == nil {
			wantResources = []string{}
		}
		if ev.Version != "0" || ev.ID != e.Forwarded.ID || ev.DetailType != e.DetailType || ev.Source != e.Source ||
			ev.Account != e.Forwarded.Account || ev.Time != at.Format("2006-01-02T15:04:05Z") || ev.Region != e.Forwarded.Region ||
			!slices.Equal(ev.Resources, wantResources) || !bytes.Equal(ev.Detail, wantDetail.Bytes()) {
			t.Fatalf("Envelope = %s, want the members of %+v with %+v", got, e, *e.Forwarded)
		}
	})
}

// The context carries one Forwarded from the outbox's events-to-events
// attempt to the PutEvents handler: what WithForwarded stores,
// ForwardedFrom returns, and a context without one reports none.
func TestForwardedContextRoundTrip(t *testing.T) {
	if f, ok := delivery.ForwardedFrom(context.Background()); ok || f != (delivery.Forwarded{}) {
		t.Errorf("ForwardedFrom(Background) = %+v, %t; want none", f, ok)
	}
	rapid.Check(t, func(t *rapid.T) {
		f := delivery.Forwarded{
			ID:      rapid.StringMatching(`[0-9a-f-]{0,36}`).Draw(t, "id"),
			Account: rapid.StringMatching(`[0-9]{12}`).Draw(t, "account"),
			Region:  rapid.SampledFrom([]string{"us-east-1", "eu-west-1"}).Draw(t, "region"),
			Time:    time.Unix(rapid.Int64Range(0, 4e9).Draw(t, "unix"), 0).UTC(),
		}
		type other struct{}
		ctx := context.WithValue(context.Background(), other{}, "unrelated")
		got, ok := delivery.ForwardedFrom(delivery.WithForwarded(ctx, f))
		if !ok || got != f {
			t.Fatalf("ForwardedFrom(WithForwarded(ctx, %+v)) = %+v, %t", f, got, ok)
		}
		if _, ok := delivery.ForwardedFrom(ctx); ok {
			t.Fatal("WithForwarded changed the parent context")
		}
	})
}
