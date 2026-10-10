package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Event is one message of the events stream. The legacy status, id and from
// members are gone since API 1.52, so only these are read (containers.md
// 2.7). Actor.Attributes merges the image's labels with the container's.
type Event struct {
	Type     string `json:"Type"`
	Action   string `json:"Action"`
	Actor    Actor  `json:"Actor"`
	Scope    string `json:"scope"`
	Time     int64  `json:"time"`
	TimeNano int64  `json:"timeNano"`
}

// Actor is the object an event is about.
type Actor struct {
	ID         string            `json:"ID"`
	Attributes map[string]string `json:"Attributes"`
}

// EventsOptions selects the events to stream.
type EventsOptions struct {
	// Filters such as {"type": ["container"], "event": ["die", "oom"],
	// "label": ["understudy.dev/managed=true"]}.
	Filters Filters
	// Since replays events from this instant on the daemon's clock; zero
	// replays nothing, so only events after the daemon subscribes arrive.
	// Events returning does not mean the daemon has subscribed: it sends the
	// response headers first (moby's getEvents), so an event emitted just
	// after Events returns can be lost without a Since. Callers that must
	// not miss an event pass a Since before the action they watch. After a
	// dropped stream, reconnect with the last TimeNano and de-duplicate on
	// (Actor.ID, Action, TimeNano).
	Since time.Time
}

// EventStream is an open events subscription. Close it when done.
type EventStream struct {
	body io.ReadCloser
	dec  *json.Decoder
	op   string
}

// Events subscribes to the daemon's events until ctx ends or the stream is
// closed.
func (c *Client) Events(ctx context.Context, opts EventsOptions) (*EventStream, error) {
	q, err := opts.Filters.query()
	if err != nil {
		return nil, err
	}
	if !opts.Since.IsZero() {
		q.Set("since", unixTimestamp(opts.Since))
	}
	resp, err := c.call(ctx, "Events", http.MethodGet, "/events", q, nil, "") //nolint:bodyclose // the returned EventStream owns the body; Close closes it
	if err != nil {
		return nil, err
	}
	return &EventStream{body: resp.Body, dec: json.NewDecoder(resp.Body), op: "Events"}, nil
}

// Next blocks for the next event. It returns io.EOF when the daemon ends
// the stream and an error wrapping the context's error after cancellation.
func (s *EventStream) Next() (Event, error) {
	var e Event
	if err := s.dec.Decode(&e); err != nil {
		if errors.Is(err, io.EOF) {
			return Event{}, io.EOF
		}
		return Event{}, fmt.Errorf("docker %s: read stream: %w", s.op, err)
	}
	return e, nil
}

// Close ends the subscription.
func (s *EventStream) Close() error { return s.body.Close() }
