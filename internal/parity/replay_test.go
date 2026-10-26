package parity

import (
	"testing"
	"time"
)

// ServerTransport opens a fresh in-process understudy for one Run under the
// SigV4 mode sigv4 ("parse" or "verify"), signing as account's root with
// the dummy secret (ADR 0012) at the emulator clock's time. Not implemented
// yet: every call of the transport it returns fails.
func ServerTransport(tb testing.TB, account, sigv4 string) Transport {
	tb.Helper()
	return unbuiltTransport{op: "ServerTransport(" + account + ", " + sigv4 + ")"}
}

// unbuiltTransport is ServerTransport's stand-in until the replayer is
// built.
type unbuiltTransport struct {
	op string
}

func (u unbuiltTransport) Send(Outgoing) (Response, string, error) {
	return Response{}, "", notImplemented(u.op)
}

func (u unbuiltTransport) Call(string, string, []byte) (Response, error) {
	return Response{}, notImplemented(u.op)
}

func (u unbuiltTransport) Advance(time.Duration) error { return notImplemented(u.op) }
