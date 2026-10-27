package edge

import (
	"net/http"
	"strings"
)

// csp is the Content-Security-Policy the edge imposes on a route's answer
// (ADR 0020).
type csp uint8

const (
	// cspAsWritten passes on whatever policy the answer sets: understudy
	// wrote the content, or a user's page is served on a host of its own,
	// an origin apart, as AWS serves function URLs and execute-api.
	cspAsWritten csp = iota
	// cspSandbox replaces any policy with sandboxPolicy: a user's page on a
	// path fallback under /_understudy/ shares the admin API's origin, so
	// it must run in an opaque origin that cannot read admin or UI answers.
	cspSandbox
)

// sandboxPolicy is the policy of a user's page on a path fallback (ADR
// 0020): sandbox with no allow-* token, so in particular no
// allow-same-origin, which would hand the page understudy's origin back.
const sandboxPolicy = "sandbox"

// policyOf is the policy the edge imposes on rt's answer.
func policyOf(rt route) csp {
	switch rt := rt.(type) {
	case toLink:
		return rt.policy
	case toExecuteAPI:
		return rt.policy
	case toAdmin, toOperation, toUnknown, toRaw, toService, notEmulated, misscoped, unclaimed, toLocalStack:
	}
	return cspAsWritten
}

// imposePolicy wraps w so the answer carries the policy p imposes.
func imposePolicy(w http.ResponseWriter, p csp) http.ResponseWriter {
	switch p {
	case cspSandbox:
		return &sandboxWriter{ResponseWriter: w}
	case cspAsWritten:
	}
	return w
}

// sandboxWriter sends exactly one Content-Security-Policy, sandboxPolicy,
// over any the handler or the edge set, whatever the status and however
// the header goes out: WriteHeader, the implicit 200 of a Write, or a
// flush.
type sandboxWriter struct {
	http.ResponseWriter
	// sent is whether a final (non-1xx) status went out.
	sent bool
}

func (s *sandboxWriter) WriteHeader(status int) {
	s.impose()
	if status >= http.StatusOK {
		s.sent = true
	}
	s.ResponseWriter.WriteHeader(status)
}

func (s *sandboxWriter) Write(b []byte) (int, error) {
	if !s.sent {
		s.WriteHeader(http.StatusOK)
	}
	return s.ResponseWriter.Write(b)
}

// FlushError sends the header before flushing, since a flush before
// WriteHeader sends an implicit 200 that would bypass WriteHeader.
func (s *sandboxWriter) FlushError() error {
	if !s.sent {
		s.WriteHeader(http.StatusOK)
	}
	return http.NewResponseController(s.ResponseWriter).Flush()
}

// Unwrap lets http.ResponseController reach the connection's writer.
func (s *sandboxWriter) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// impose drops every Content-Security-Policy entry, whatever the case of
// its key, since net/http sends each map key as written, then sets the
// sandbox.
func (s *sandboxWriter) impose() {
	h := s.Header()
	for name := range h {
		if strings.EqualFold(name, "Content-Security-Policy") {
			delete(h, name)
		}
	}
	h.Set("Content-Security-Policy", sandboxPolicy)
}
