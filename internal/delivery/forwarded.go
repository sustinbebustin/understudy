package delivery

import (
	"context"
	"errors"
)

// errEnvelopeNotImplemented is what Envelope answers until it writes the
// forwarded event.
var errEnvelopeNotImplemented = errors.New("delivery: EventEntry.Envelope is not implemented yet, so no event envelope was written")

// Envelope is the event as the destination bus receives it, JSON: the body
// of an events-to-events dead letter. It is not implemented yet and always
// fails.
func (e EventEntry) Envelope() ([]byte, error) {
	return nil, errEnvelopeNotImplemented
}

// WithForwarded returns ctx carrying f, for the one entry of an
// events-to-events attempt's PutEvents call. It is not implemented yet and
// returns ctx unchanged.
func WithForwarded(ctx context.Context, _ Forwarded) context.Context {
	return ctx
}

// ForwardedFrom returns the Forwarded ctx carries. It is not implemented
// yet and reports none.
func ForwardedFrom(context.Context) (Forwarded, bool) {
	return Forwarded{}, false
}
