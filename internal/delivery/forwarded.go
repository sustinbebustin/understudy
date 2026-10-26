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
