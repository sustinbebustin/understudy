package admin

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// ErrorCode is the closed set of admin API error codes. The CLI maps each to
// its exit code through ExitCode and never matches on message text.
type ErrorCode string

// ErrorCode values.
const (
	// InvalidRequest is a request the endpoint cannot accept as written: a
	// malformed body, an unknown parameter value, a fault rule that fails
	// validation, a clock move backwards.
	InvalidRequest ErrorCode = "invalid_request"
	// Unauthorized is an admin call without the bearer token a non-loopback
	// listener requires, or with the wrong one.
	Unauthorized ErrorCode = "unauthorized"
	// NotFound is an endpoint, fault rule, trace, principal or resource that
	// does not exist.
	NotFound ErrorCode = "not_found"
	// Conflict is a request that clashes with the emulator's state, such as
	// a snapshot whose schema versions this build does not run, or a
	// snapshot archive that is damaged or not a snapshot at all.
	Conflict ErrorCode = "conflict"
	// ConditionNotMet is a wait that ended before its condition held: settle
	// timing out with work still pending.
	ConditionNotMet ErrorCode = "condition_not_met"
	// Unavailable is a feature this server does not offer as configured,
	// such as the snapshot path form without persistence, or one the build
	// does not have yet.
	Unavailable ErrorCode = "unavailable"
	// Internal is a failure inside the emulator; the server log has detail.
	Internal ErrorCode = "internal"
)

// Exit codes of the understudy CLI (ARCHITECTURE.md 17).
const (
	ExitOK              = 0
	ExitFailed          = 1
	ExitUsage           = 2
	ExitUnreachable     = 3
	ExitConditionNotMet = 4
	// ExitUnauthorized is a call the server refused for its admin token,
	// kept apart from usage so a script can tell a bad token from a bad
	// command line.
	ExitUnauthorized = 5
)

// ExitCode returns the CLI exit code for c: 2 for a request the caller must
// change (invalid_request), 5 for unauthorized, 4 for condition_not_met, 1
// for the rest.
func (c ErrorCode) ExitCode() int {
	if _, exit, ok := c.table(); ok {
		return exit
	}
	return ExitFailed
}

// HTTPStatus returns the status the server answers c with.
func (c ErrorCode) HTTPStatus() int {
	if status, _, ok := c.table(); ok {
		return status
	}
	return http.StatusInternalServerError
}

// UnmarshalText accepts only the codes of the closed set.
func (c *ErrorCode) UnmarshalText(text []byte) error {
	code := ErrorCode(text)
	if _, _, ok := code.table(); !ok {
		return &CodeError{Text: string(text)}
	}
	*c = code
	return nil
}

// table is the single table from an ErrorCode to its HTTP status and CLI
// exit code; ok is false for a code outside the closed set.
func (c ErrorCode) table() (status, exit int, ok bool) {
	switch c {
	case InvalidRequest:
		return http.StatusBadRequest, ExitUsage, true
	case Unauthorized:
		return http.StatusUnauthorized, ExitUnauthorized, true
	case NotFound:
		return http.StatusNotFound, ExitFailed, true
	case Conflict:
		return http.StatusConflict, ExitFailed, true
	case ConditionNotMet:
		return http.StatusPreconditionFailed, ExitConditionNotMet, true
	case Unavailable:
		return http.StatusServiceUnavailable, ExitFailed, true
	case Internal:
		return http.StatusInternalServerError, ExitFailed, true
	default:
		return 0, 0, false
	}
}

// CodeError reports an error code outside the closed set, such as one a
// newer server sent.
type CodeError struct {
	Text string
}

func (e *CodeError) Error() string {
	return fmt.Sprintf("admin: %q is not an admin error code; known codes are %s", e.Text,
		strings.Join([]string{
			string(InvalidRequest), string(Unauthorized), string(NotFound), string(Conflict),
			string(ConditionNotMet), string(Unavailable), string(Internal),
		}, ", "))
}

// Error is an admin API error as the envelope {"error": {...}} carries it.
// Field, Known and Pending are set only by the endpoints that name them.
type Error struct {
	Code    ErrorCode `json:"code"`
	Message string    `json:"message"`
	// Hint is the next thing to try, such as the command that fixes it.
	Hint string `json:"hint,omitempty"`
	// Field is the request member at fault, spelled as in its JSON form,
	// such as "action.shape" on a fault rule.
	Field string `json:"field,omitempty"`
	// Known lists the values the request may use instead: the error shapes
	// the operation really sends, for a fault rule naming another.
	Known []string `json:"known,omitempty"`
	// Pending is the work still outstanding by kind when settle times out.
	Pending map[string]int `json:"pending,omitempty"`
}

func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString(string(e.Code))
	b.WriteString(": ")
	b.WriteString(e.Message)
	if len(e.Known) > 0 {
		b.WriteString(" (known: ")
		b.WriteString(strings.Join(e.Known, ", "))
		b.WriteString(")")
	}
	if e.Hint != "" {
		b.WriteString("; ")
		b.WriteString(e.Hint)
	}
	return b.String()
}

// Envelope is the body of every admin error response.
type Envelope struct {
	Error *Error `json:"error"`
}

// UnreachableError is an admin call that got no HTTP response: the
// emulator is not running at Endpoint, or the connection failed.
type UnreachableError struct {
	Endpoint string
	Err      error
}

func (e *UnreachableError) Error() string {
	return fmt.Sprintf("admin: understudy is not reachable at %s: %v; start it with `understudy serve`, or point --endpoint or UNDERSTUDY_ENDPOINT at the running server",
		e.Endpoint, e.Err)
}

func (e *UnreachableError) Unwrap() error { return e.Err }

// ExitCode returns the CLI exit code for the result of an admin call: 0 for
// nil, the code's own exit code for an *Error, 3 for an *UnreachableError
// and 1 for anything else.
func ExitCode(err error) int {
	if err == nil {
		return ExitOK
	}
	if adminErr, ok := errors.AsType[*Error](err); ok {
		return adminErr.Code.ExitCode()
	}
	if unreachable, ok := errors.AsType[*UnreachableError](err); ok && unreachable != nil {
		return ExitUnreachable
	}
	return ExitFailed
}
