package delivery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// ErrNotForwarded is Envelope's answer for an entry with no Forwarded.
var ErrNotForwarded = errors.New("delivery: the event entry has no Forwarded, so it is a new event without an id, account, region or time, and has no envelope")

// envelope is a delivered event's members in the recorded order
// (secrets-logs-events-alarms.md 4.3), the order the events service's
// envelope.Event.JSON writes.
type envelope struct {
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

// Envelope is the event as the destination bus receives it, compact JSON:
// the body of an events-to-events dead letter. The time is written in UTC
// to the second, and resources as [] when there are none
// (secrets-logs-events-alarms.md 4.3, 4.7). It fails with ErrNotForwarded
// for an entry with no Forwarded, and with a *DetailError for a Detail that
// is not one JSON object.
func (e EventEntry) Envelope() ([]byte, error) {
	if e.Forwarded == nil {
		return nil, ErrNotForwarded
	}
	detail, err := compactObject(e.Detail)
	if err != nil {
		return nil, err
	}
	resources := e.Resources
	if resources == nil {
		resources = []string{}
	}
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	// The events service's encoder (jsonx) escapes only the quote, the
	// backslash and control characters, so <, > and & are written as they
	// are. encoding/json still escapes U+2028 and U+2029 and replaces
	// invalid UTF-8; both decode to what jsonx's text decodes to for valid
	// UTF-8.
	enc.SetEscapeHTML(false)
	if err := enc.Encode(envelope{
		Version: "0", ID: e.Forwarded.ID, DetailType: e.DetailType, Source: e.Source,
		Account: e.Forwarded.Account, Time: e.Forwarded.Time.UTC().Format("2006-01-02T15:04:05Z"),
		Region: e.Forwarded.Region, Resources: resources, Detail: detail,
	}); err != nil {
		return nil, fmt.Errorf("delivery: writing the event envelope: %w", err)
	}
	return bytes.TrimSuffix(out.Bytes(), []byte("\n")), nil
}

// DetailError is an event entry's Detail that is not one JSON object, as an
// event's detail must be (MalformedDetail, secrets-logs-events-alarms.md
// 4.7).
type DetailError struct {
	Detail string
	// Err is the syntax error; nil when the text is JSON but not an object.
	Err error
}

func (e *DetailError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("delivery: the event entry's Detail %q is not JSON (%v), so the event has no envelope; an event's detail is one JSON object", e.Detail, e.Err)
	}
	return fmt.Sprintf("delivery: the event entry's Detail %q is JSON but not an object, so the event has no envelope; an event's detail is one JSON object", e.Detail)
}

func (e *DetailError) Unwrap() error { return e.Err }

// compactObject returns detail compacted, or a *DetailError when it is not
// one JSON object.
func compactObject(detail string) (json.RawMessage, error) {
	var out bytes.Buffer
	if err := json.Compact(&out, []byte(detail)); err != nil {
		return nil, &DetailError{Detail: detail, Err: err}
	}
	if b := out.Bytes(); len(b) == 0 || b[0] != '{' {
		return nil, &DetailError{Detail: detail}
	}
	return out.Bytes(), nil
}

// forwardedKey is the context key of a Forwarded. Being unexported, only
// WithForwarded can set the value.
type forwardedKey struct{}

// WithForwarded returns ctx carrying f, for the one entry of an
// events-to-events attempt's PutEvents call. The envelope cannot ride the
// PutEvents wire input, which has no id, account or region member, so it
// travels beside the call.
func WithForwarded(ctx context.Context, f Forwarded) context.Context {
	return context.WithValue(ctx, forwardedKey{}, f)
}

// ForwardedFrom returns the Forwarded ctx carries, and false when it
// carries none.
func ForwardedFrom(ctx context.Context) (Forwarded, bool) {
	f, ok := ctx.Value(forwardedKey{}).(Forwarded)
	return f, ok
}
