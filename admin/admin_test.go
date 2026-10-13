package admin_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/admin"
)

// Every code maps to one HTTP status and one CLI exit code, through the
// single table: 2 for a request the caller must change, 5 for a refused
// token, 4 for a condition that did not hold, 1 for the rest
// (ARCHITECTURE.md 17).
func TestErrorCodeTable(t *testing.T) {
	cases := []struct {
		code   admin.ErrorCode
		status int
		exit   int
	}{
		{admin.InvalidRequest, http.StatusBadRequest, admin.ExitUsage},
		{admin.Unauthorized, http.StatusUnauthorized, admin.ExitUnauthorized},
		{admin.NotFound, http.StatusNotFound, admin.ExitFailed},
		{admin.Conflict, http.StatusConflict, admin.ExitFailed},
		{admin.ConditionNotMet, http.StatusPreconditionFailed, admin.ExitConditionNotMet},
		{admin.Unavailable, http.StatusServiceUnavailable, admin.ExitFailed},
		{admin.Internal, http.StatusInternalServerError, admin.ExitFailed},
	}
	for _, c := range cases {
		if got := c.code.HTTPStatus(); got != c.status {
			t.Errorf("%s.HTTPStatus() = %d, want %d", c.code, got, c.status)
		}
		if got := c.code.ExitCode(); got != c.exit {
			t.Errorf("%s.ExitCode() = %d, want %d", c.code, got, c.exit)
		}
		err := fmt.Errorf("calling the server: %w", &admin.Error{Code: c.code, Message: "m"})
		if got := admin.ExitCode(err); got != c.exit {
			t.Errorf("ExitCode(wrapped %s) = %d, want %d", c.code, got, c.exit)
		}
	}
}

func TestExitCodeOfOtherResults(t *testing.T) {
	if got := admin.ExitCode(nil); got != admin.ExitOK {
		t.Errorf("ExitCode(nil) = %d", got)
	}
	if got := admin.ExitCode(&admin.UnreachableError{Endpoint: "http://127.0.0.1:1", Err: errors.New("refused")}); got != admin.ExitUnreachable {
		t.Errorf("ExitCode(unreachable) = %d", got)
	}
	if got := admin.ExitCode(errors.New("other")); got != admin.ExitFailed {
		t.Errorf("ExitCode(other) = %d", got)
	}
}

// A code outside the closed set does not decode, so a client never acts on
// a code it does not know.
func TestErrorCodeRefusesUnknownCodes(t *testing.T) {
	var env admin.Envelope
	err := json.Unmarshal([]byte(`{"error":{"code":"teapot","message":"m"}}`), &env)
	var codeErr *admin.CodeError
	if !errors.As(err, &codeErr) || codeErr.Text != "teapot" {
		t.Fatalf("decoding an unknown code: %v", err)
	}
}

func clockOp() *rapid.Generator[admin.ClockOp] {
	return rapid.OneOf(
		rapid.Just[admin.ClockOp](admin.Freeze{}),
		rapid.Just[admin.ClockOp](admin.Resume{}),
		rapid.Map(rapid.Int64Range(0, int64(24*time.Hour)), func(n int64) admin.ClockOp { return admin.Advance{By: time.Duration(n)} }),
		rapid.Map(rapid.Int64Range(0, 4102444800), func(s int64) admin.ClockOp { return admin.Set{To: time.Unix(s, 0).UTC()} }),
		rapid.Map(rapid.Float64Range(0.001, 10000), func(f float64) admin.ClockOp { return admin.Scale{Factor: f} }),
	)
}

// Every clock op reads back as itself from its JSON form.
func TestClockOpRoundTrip(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		op := clockOp().Draw(t, "op")
		data, err := json.Marshal(op)
		if err != nil {
			t.Fatal(err)
		}
		got, err := admin.ParseClockOp(data)
		if err != nil {
			t.Fatalf("ParseClockOp(%s) = %v", data, err)
		}
		if !reflect.DeepEqual(got, op) {
			t.Fatalf("ParseClockOp(%s) = %#v, want %#v", data, got, op)
		}
	})
}

// A clock op with a member its op does not take, or without one it needs,
// is refused naming the member, rather than half applied.
func TestParseClockOpRefusesMismatchedMembers(t *testing.T) {
	for _, body := range []string{
		`{"op":"advance"}`,
		`{"op":"advance","by":"1s","factor":2}`,
		`{"op":"freeze","by":"1s"}`,
		`{"op":"resume","factor":2}`,
		`{"op":"set","by":"1s"}`,
		`{"op":"rewind"}`,
		`{"op":"scale","factor":2,"extra":1}`,
	} {
		op, err := admin.ParseClockOp([]byte(body))
		if e, ok := errors.AsType[*admin.Error](err); op != nil || !ok || e.Code != admin.InvalidRequest {
			t.Errorf("ParseClockOp(%s) = %#v, %v; want no op and invalid_request", body, op, err)
		}
	}
}

// A clock op that is valid JSON but carries a bad value is refused naming
// the member and the value, not as a JSON syntax error; only a body that is
// not JSON at all is reported as such.
func TestParseClockOpNamesTheBadMember(t *testing.T) {
	for _, tc := range []struct {
		body, field, message string
	}{
		{`{"op":"advance","by":"5"}`, "by", `"5" is not a duration such as "90s" or "1h30m"`},
		{`{"op":"advance","by":5}`, "by", `5 is not a duration such as "90s" or "1h30m"`},
		{`{"op":"set","to":"yesterday"}`, "to", `"yesterday" is not an RFC 3339 time such as "2026-12-31T23:59:00Z"`},
		{`{"op":"scale","factor":"60"}`, "factor", `"60" is not a number such as 60`},
		{`{"op":7}`, "op", `7 is not a clock op`},
		{`{"op":"scale","factor":2,"extra":1}`, "extra", `"extra" is not a clock op member; the members are op, by, to and factor`},
		{`["advance"]`, "", `the clock op is a JSON array, not an object`},
		{`{"op":"advance","by":"90s"`, "", `the clock op is not valid JSON: unexpected EOF`},
		{`{"op":"freeze"} {}`, "", `the clock op is not valid JSON: unexpected data after the JSON value`},
	} {
		op, err := admin.ParseClockOp([]byte(tc.body))
		e, ok := errors.AsType[*admin.Error](err)
		if op != nil || !ok {
			t.Errorf("ParseClockOp(%s) = %#v, %v; want no op and an *admin.Error", tc.body, op, err)
			continue
		}
		if e.Code != admin.InvalidRequest || e.Field != tc.field || e.Message != tc.message {
			t.Errorf("ParseClockOp(%s) = code %q field %q message %q; want code %q field %q message %q",
				tc.body, e.Code, e.Field, e.Message, admin.InvalidRequest, tc.field, tc.message)
		}
	}
}

// faultRule draws fault rules covering every action and trigger.
func faultRule() *rapid.Generator[admin.FaultRule] {
	shape := rapid.SampledFrom([]string{"com.amazonaws.sqs#QueueDoesNotExist", "QueueDoesNotExist"})
	action := rapid.OneOf(
		rapid.Custom(func(t *rapid.T) admin.FaultAction {
			return admin.InjectError{Shape: shape.Draw(t, "shape"), Message: rapid.StringMatching(`[a-z ]{0,8}`).Draw(t, "message")}
		}),
		rapid.Just[admin.FaultAction](admin.Throttle{}),
		rapid.Map(rapid.Int64Range(1, int64(time.Minute)), func(n int64) admin.FaultAction {
			return admin.Latency{Wall: time.Duration(n)}
		}),
		rapid.Map(shape, func(s string) admin.FaultAction { return admin.AfterCommit{Shape: s} }),
		rapid.Map(shape, func(s string) admin.FaultAction { return admin.StatusOKError{Shape: s} }),
		rapid.Just[admin.FaultAction](admin.DropDelivery{}),
	)
	trigger := rapid.OneOf(
		rapid.Just[admin.FaultTrigger](admin.Always{}),
		rapid.Map(rapid.IntRange(1, 100), func(n int) admin.FaultTrigger { return admin.NextN{N: n} }),
		rapid.Map(rapid.Float64Range(0, 1), func(p float64) admin.FaultTrigger { return admin.Probability{P: p} }),
	)
	names := rapid.SliceOfN(rapid.StringMatching(`[a-z]{1,6}`), 0, 2)
	return rapid.Custom(func(t *rapid.T) admin.FaultRule {
		return admin.FaultRule{
			ID:    rapid.StringMatching(`[a-z0-9-]{0,12}`).Draw(t, "id"),
			Phase: rapid.SampledFrom([]admin.FaultPhase{admin.RequestPhase, admin.DeliveryPhase}).Draw(t, "phase"),
			Match: admin.FaultMatch{
				Services:   names.Draw(t, "services"),
				Operations: names.Draw(t, "operations"),
				Resource:   rapid.StringMatching(`(arn:aws:sqs:\*:\*:[a-z]{1,4}\*)?`).Draw(t, "resource"),
				Origin:     rapid.SampledFrom([]admin.FaultOrigin{"", admin.ExternalOrigin, admin.InternalOrigin}).Draw(t, "origin"),
			},
			Action:  action.Draw(t, "action"),
			Trigger: trigger.Draw(t, "trigger"),
		}
	})
}

// Every fault rule reads back as itself from its JSON form.
func TestFaultRuleRoundTrip(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		rule := faultRule().Draw(t, "rule")
		data, err := json.Marshal(rule)
		if err != nil {
			t.Fatal(err)
		}
		var got admin.FaultRule
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatalf("decoding %s: %v", data, err)
		}
		if !reflect.DeepEqual(normalize(got), normalize(rule)) {
			t.Fatalf("round trip of %s = %#v, want %#v", data, got, rule)
		}
	})
}

// normalize makes empty and nil lists compare equal, as JSON omits both.
func normalize(r admin.FaultRule) admin.FaultRule {
	empty := func(s []string) []string {
		if len(s) == 0 {
			return nil
		}
		return s
	}
	r.Match.Services, r.Match.Operations = empty(r.Match.Services), empty(r.Match.Operations)
	return r
}

// A rule without an action or trigger, with an unknown kind, or with a
// member its kind does not take (which fault.Rule refuses too), does not
// decode, and the error names the member.
func TestFaultRuleRefusesIncompleteRules(t *testing.T) {
	for body, field := range map[string]string{
		`{"phase":"request","trigger":{"kind":"always"}}`:                                                                            "action",
		`{"phase":"request","action":{"kind":"throttle"}}`:                                                                           "trigger",
		`{"phase":"request","action":{"kind":"explode"},"trigger":{"kind":"always"}}`:                                                "action.kind",
		`{"phase":"request","action":{"kind":"throttle"},"trigger":{"kind":"next-n"}}`:                                               "trigger.n",
		`{"phase":"request","action":{"kind":"latency","wall":"soon"},"trigger":{"kind":"always"}}`:                                  "action.wall",
		`{"phase":"request","action":{"kind":"throttle","shape":"com.amazonaws.sqs#QueueDoesNotExist"},"trigger":{"kind":"always"}}`: "action.shape",
		`{"phase":"request","action":{"kind":"throttle","shape":""},"trigger":{"kind":"always"}}`:                                    "action.shape",
		`{"phase":"delivery","action":{"kind":"drop-delivery","wall":"1s"},"trigger":{"kind":"always"}}`:                             "action.wall",
		`{"phase":"request","action":{"kind":"latency","wall":"1s","message":"slow"},"trigger":{"kind":"always"}}`:                   "action.message",
		`{"phase":"request","action":{"kind":"after-commit","shape":"X","message":"m"},"trigger":{"kind":"always"}}`:                 "action.message",
		`{"phase":"request","action":{"kind":"status-ok-error","shape":"X","wall":"1s"},"trigger":{"kind":"always"}}`:                "action.wall",
		`{"phase":"request","action":{"kind":"error","shape":"X","wall":"1s"},"trigger":{"kind":"always"}}`:                          "action.wall",
		`{"phase":"request","action":{"kind":"throttle"},"trigger":{"kind":"always","n":3}}`:                                         "trigger.n",
		`{"phase":"request","action":{"kind":"throttle"},"trigger":{"kind":"next-n","n":3,"p":0.5}}`:                                 "trigger.p",
		`{"phase":"request","action":{"kind":"throttle"},"trigger":{"kind":"probability","p":0.5,"n":3}}`:                            "trigger.n",
	} {
		var r admin.FaultRule
		err := json.Unmarshal([]byte(body), &r)
		var form *admin.FaultFormError
		if !errors.As(err, &form) || form.Field != field {
			t.Errorf("decoding %s = %v, want a FaultFormError on %s", body, err, field)
		}
	}
}

func TestNewRefusesUnusableEndpoints(t *testing.T) {
	for _, endpoint := range []string{"127.0.0.1:4566", "ftp://127.0.0.1:4566", "http://", "http://127.0.0.1:4566/prefix", "http://127.0.0.1:4566?x=1"} {
		var e *admin.EndpointError
		if _, err := admin.New(endpoint, admin.Options{}); !errors.As(err, &e) {
			t.Errorf("New(%q) = %v, want an EndpointError", endpoint, err)
		}
	}
	if _, err := admin.New("http://127.0.0.1:4566/", admin.Options{}); err != nil {
		t.Errorf("New with a trailing slash: %v", err)
	}
}

// A client whose server is not running answers unreachable, exit code 3.
func TestClientReportsUnreachableServers(t *testing.T) {
	var lc net.ListenConfig
	l, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	endpoint := "http://" + l.Addr().String()
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	c, err := admin.New(endpoint, admin.Options{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Health(t.Context())
	if admin.ExitCode(err) != admin.ExitUnreachable {
		t.Fatalf("Health against a closed port = %v, want unreachable", err)
	}
}

// Health reads a 503 readiness answer as a server still starting, and any
// other answer without an admin envelope, such as a proxy's error page, as
// the wrong endpoint.
func TestHealthTellsStartingFromWrongEndpoints(t *testing.T) {
	answer := func(status int, body string) *admin.Client {
		t.Helper()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
			fmt.Fprint(w, body)
		}))
		t.Cleanup(srv.Close)
		c, err := admin.New(srv.URL, admin.Options{})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	h, err := answer(http.StatusServiceUnavailable, `{"ready":false,"waiting":["listener"]}`).Health(t.Context())
	if err != nil || h.Ready || len(h.Waiting) != 1 {
		t.Fatalf("Health of a starting server = %+v, %v", h, err)
	}
	for _, tc := range []struct {
		status int
		body   string
	}{
		{http.StatusNotFound, "<html>not found</html>"},
		{http.StatusBadGateway, "<html>bad gateway</html>"},
		{http.StatusServiceUnavailable, "<html>maintenance</html>"},
	} {
		h, err := answer(tc.status, tc.body).Health(t.Context())
		var e *admin.Error
		if h != nil || !errors.As(err, &e) || !strings.Contains(e.Hint, "--endpoint") {
			t.Errorf("Health answered %d %s = %+v, %v; want the wrong-endpoint error", tc.status, tc.body, h, err)
		}
	}
}
