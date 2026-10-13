package admin_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/admin"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	serveradmin "github.com/sustinbebustin/understudy/internal/server/admin"
)

// envelope decodes rec's error envelope, failing when it holds none.
func envelope(k *kerneltest.K, rec *httptest.ResponseRecorder) *admin.Error {
	k.Helper()
	var env admin.Envelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil || env.Error == nil {
		k.Fatalf("answer %d %s holds no error envelope: %v", rec.Code, rec.Body, err)
	}
	return env.Error
}

// A body member the endpoint has no field for is refused naming the member
// and the members it does have, so "service" for "services" says what to
// send; nothing is reset. Members match without regard to case.
func TestUnknownBodyMembersNameTheKnownOnes(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		svc := &lateService{}
		h := handlerWith(k, func(o *serveradmin.Options) { o.Services = []serveradmin.Service{svc} })
		rec := reset(k, h, `{"service": ["late"]}`)
		e := envelope(k, rec)
		if rec.Code != http.StatusBadRequest || e.Code != admin.InvalidRequest || e.Field != "service" ||
			!slices.Equal(e.Known, []string{"services", "accounts", "regions", "clock", "faults"}) || len(svc.resets) != 0 {
			k.Fatalf("reset with an unknown member = %d %+v, resets %v; want 400 naming it and the known members", rec.Code, e, svc.resets)
		}
		if rec := reset(k, h, `{"Services": ["late"]}`); rec.Code != http.StatusOK || len(svc.resets) != 1 {
			k.Fatalf("reset with a member in another case = %d %s, want 200 as encoding/json reads it", rec.Code, rec.Body)
		}
		if rec := reset(k, h, `["late"]`); rec.Code != http.StatusBadRequest || !strings.Contains(envelope(k, rec).Message, "not the expected JSON") {
			k.Fatalf("reset with a body that is not an object = %d %s, want the decoder's 400", rec.Code, rec.Body)
		}
	})
}

// A path that is no endpoint answers 404 listing the listener's endpoints,
// with the captured segments named; the container listener lists only its
// own two.
func TestNotFoundListsTheEndpoints(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		h := handler(k, "")
		rec := serve(k, h, http.MethodGet, admin.Prefix+"/nope")
		e := envelope(k, rec)
		for _, want := range []string{"GET /health", "POST /reset", "PUT /faults/{id}", "POST /lambda/functions/{function}/hot-reload"} {
			if rec.Code != http.StatusNotFound || e.Code != admin.NotFound || !strings.Contains(e.Hint, want) {
				k.Fatalf("GET /nope = %d %+v; want 404 whose hint lists %q", rec.Code, e, want)
			}
		}
		rec = serve(k, h.Container(), http.MethodGet, admin.Prefix+"/nope")
		if e := envelope(k, rec); rec.Code != http.StatusNotFound || e.Hint != "the endpoints under "+admin.Prefix+" are GET /health, GET /clock" {
			k.Fatalf("GET /nope on the container listener = %d %+v; want 404 listing health and clock only", rec.Code, e)
		}
	})
}

// A rule naming an operation outside service.Operation form is refused in
// those terms, not the decoder's Go types.
func TestFaultRuleOperationFormIsNamed(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		rule := `{"phase": "request", "match": {"operations": ["SendMessage"]}, "action": {"kind": "throttle"}, "trigger": {"kind": "always"}}`
		req := httptest.NewRequestWithContext(k.Context(), http.MethodPut, admin.Prefix+"/faults/f1", strings.NewReader(rule))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		handler(k, "").ServeHTTP(rec, req)
		e := envelope(k, rec)
		if e.Code != admin.InvalidRequest || e.Field != "match.operations" || !strings.Contains(e.Message, `"SendMessage" is not an operation`) ||
			strings.Contains(e.Message, "json:") || strings.Contains(e.Message, "fault.") {
			k.Errorf("PUT a rule with operation SendMessage = %+v, want invalid_request on match.operations naming the form only", e)
		}
	})
}

// A call without the token is told the header it needs; one with a wrong
// token that it was rejected.
func TestUnauthorizedTellsMissingFromRejected(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		h := handler(k, "s3cret")
		missing := envelope(k, serve(k, h, http.MethodGet, admin.Prefix+"/clock"))
		if missing.Code != admin.Unauthorized || !strings.Contains(missing.Message, "sent no Authorization header") {
			k.Errorf("GET clock without Authorization = %+v, want unauthorized naming the missing header", missing)
		}
		req := httptest.NewRequestWithContext(k.Context(), http.MethodGet, admin.Prefix+"/clock", http.NoBody)
		req.Header.Set("Authorization", "Bearer wrong")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rejected := envelope(k, rec); rejected.Code != admin.Unauthorized || !strings.Contains(rejected.Message, "rejected") {
			k.Errorf("GET clock with a wrong token = %+v, want unauthorized saying it was rejected", rejected)
		}
	})
}

// Admin answers are JSON as written: a hint such as "Bearer <token>" is
// not escaped as if it were bound for an HTML page.
func TestAnswersDoNotEscapeHTML(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		rec := serve(k, handler(k, "s3cret"), http.MethodGet, admin.Prefix+"/health")
		if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "Bearer <token>") {
			k.Fatalf("GET health without the token = %d %s; want 401 that reads Bearer <token>", rec.Code, rec.Body)
		}
	})
}

// An archive that is empty, damaged or not a snapshot at all is the
// file's fault, not the request's: conflict, saying once that nothing was
// loaded and how to check the file.
func TestDamagedArchivesAreConflicts(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		h := handler(k, "")
		for _, body := range []string{"", "not an archive"} {
			rec := load(k, h, strings.NewReader(body))
			e := envelope(k, rec)
			if rec.Code != http.StatusConflict || e.Code != admin.Conflict || e.Code.ExitCode() != admin.ExitFailed ||
				strings.Count(e.Message, "nothing was loaded") != 1 || !strings.Contains(e.Hint, "understudy snapshot inspect") {
				k.Errorf("load of %q = %d %+v; want 409 conflict that says once nothing was loaded and names snapshot inspect", body, rec.Code, e)
			}
		}
	})
}

// Moving the clock back is refused with a hint naming the commands that
// go back, without repeating the message's own advice.
func TestClockBackwardsHintNamesTheCommands(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		h := handler(k, "")
		past := k.Clock.Now().Wire().Add(-time.Hour).Format(time.RFC3339)
		rec := call(k, h, http.MethodPost, admin.Prefix+"/clock", "application/json", `{"op": "set", "to": "`+past+`"}`, nil)
		e := envelope(k, rec)
		if rec.Code != http.StatusBadRequest || e.Field != "to" || !strings.Contains(e.Hint, "'understudy reset --clock'") ||
			!strings.Contains(e.Hint, "'understudy snapshot load FILE'") || strings.Contains(e.Hint, "only moves forward") {
			k.Fatalf("clock set into the past = %d %+v; want 400 whose hint names reset --clock and snapshot load", rec.Code, e)
		}
	})
}

// failingPuller is an ImagePuller whose every pull fails with err.
type failingPuller struct{ err error }

func (p failingPuller) PullImages(context.Context, []string) ([]string, error) { return nil, p.err }

// unpinnedError is a pull error the request caused.
type unpinnedError struct{}

func (unpinnedError) Error() string { return `understudy pins no image for the runtime "cobol"` }
func (unpinnedError) RequestFault() {}

// A runtime the build pins no image for is the request's fault, not an
// unavailable daemon.
func TestImagePullOfAnUnpinnedRuntimeIsInvalid(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		h := handlerWith(k, func(o *serveradmin.Options) { o.ImagePuller = failingPuller{err: unpinnedError{}} })
		rec := call(k, h, http.MethodPost, admin.Prefix+"/lambda/images/pull", "", "", nil)
		if e := envelope(k, rec); rec.Code != http.StatusBadRequest || e.Code != admin.InvalidRequest || e.Field != "runtimes" {
			k.Fatalf("images pull of an unpinned runtime = %d %+v; want 400 invalid_request on runtimes", rec.Code, e)
		}
	})
}

// A failed image pull reports its cause, which names its own next step,
// without a hint that repeats it.
func TestImagePullFailureCarriesOnlyItsCause(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		cause := errors.New("no Docker daemon is reachable; start Docker, or run `understudy doctor` to diagnose")
		h := handlerWith(k, func(o *serveradmin.Options) { o.ImagePuller = failingPuller{err: cause} })
		rec := call(k, h, http.MethodPost, admin.Prefix+"/lambda/images/pull", "", "", nil)
		if e := envelope(k, rec); rec.Code != http.StatusServiceUnavailable || e.Code != admin.Unavailable ||
			!strings.HasSuffix(e.Message, cause.Error()) || e.Hint != "" {
			k.Fatalf("images pull without Docker = %d %+v; want 503 with the cause and no hint", rec.Code, e)
		}
	})
}

// An unknown service, account or region is refused naming the parameter
// as the endpoint spells it: the body member on reset, the query parameter
// on resources. The services list tells understudy's own state, which is
// no AWS service, from the services, and the hint says what selecting no
// service does at that endpoint: resources lists, it never resets.
func TestScopeErrorsNameTheEndpointsParameter(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		h := handlerWith(k, func(o *serveradmin.Options) { o.Services = []serveradmin.Service{&lateService{}} })
		cases := []struct {
			name  string
			rec   *httptest.ResponseRecorder
			field string
		}{
			{"reset services", reset(k, h, `{"services": ["zz"]}`), "services"},
			{"reset accounts", reset(k, h, `{"accounts": ["zz"]}`), "accounts"},
			{"reset regions", reset(k, h, `{"regions": ["zz"]}`), "regions"},
			{"resources service", serve(k, h, http.MethodGet, admin.Prefix+"/resources?service=zz"), "service"},
			{"resources account", serve(k, h, http.MethodGet, admin.Prefix+"/resources?account=zz"), "account"},
			{"resources region", serve(k, h, http.MethodGet, admin.Prefix+"/resources?region=zz"), "region"},
		}
		for _, c := range cases {
			if e := envelope(k, c.rec); c.rec.Code != http.StatusBadRequest || e.Field != c.field {
				k.Errorf("%s = %d %+v; want 400 on field %q", c.name, c.rec.Code, e, c.field)
			}
		}
		e := envelope(k, cases[0].rec)
		if !slices.Equal(e.Known, []string{"late"}) || !strings.Contains(e.Message, `services names "zz"`) ||
			!strings.Contains(e.Hint, "late holds understudy's own state") || !strings.Contains(e.Hint, "resets") {
			k.Errorf("reset of an unknown service = %+v; want known [late], the value named, and a hint that late is understudy's own state", e)
		}
		e = envelope(k, cases[3].rec)
		if !slices.Equal(e.Known, []string{"late"}) || !strings.Contains(e.Message, `service names "zz"`) ||
			!strings.Contains(e.Hint, "lists") || strings.Contains(e.Hint, "reset") {
			k.Errorf("resources of an unknown service = %+v; want known [late], the value named, and a hint about listing that never says reset", e)
		}
	})
}

// A rejected fault rule names the field at fault in the message, which is
// all the CLI prints of it, so the reason reads with its subject.
func TestRejectedFaultRuleNamesTheField(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		rule := `{"phase": "request", "action": {"kind": "throttle"}, "trigger": {"kind": "probability", "p": 1.5}}`
		req := httptest.NewRequestWithContext(k.Context(), http.MethodPut, admin.Prefix+"/faults/f1", strings.NewReader(rule))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		handler(k, "").ServeHTTP(rec, req)
		e := envelope(k, rec)
		if e.Code != admin.InvalidRequest || e.Field != "trigger.p" || !strings.Contains(e.Message, "no rule was added or changed: trigger.p: ") {
			k.Errorf("PUT a rule with probability 1.5 = %+v; want invalid_request whose message names trigger.p", e)
		}
	})
}

// A journal filter value that selects nothing meaningful is refused rather
// than read as no filter: a zero event id, a zero limit, and a query string
// net/url cannot parse, whose bad pair Query would silently drop.
func TestJournalRefusesValuesThatWouldMatchEverything(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		h := handler(k, "")
		for _, q := range []string{
			"events?trace=0", "events?from_id=0", "events?to_id=0", "events?cursor=0", "events?limit=0",
			"events?resource=%ZZ", "events?service=sqs;kind=api", "stream?after=0", "stream?trace=0",
		} {
			rec := serve(k, h, http.MethodGet, admin.Prefix+"/journal/"+q)
			if e := envelope(k, rec); rec.Code != http.StatusBadRequest || e.Code != admin.InvalidRequest {
				k.Errorf("GET journal/%s = %d %+v; want 400 invalid_request", q, rec.Code, e)
			}
		}
		if rec := serve(k, h, http.MethodGet, admin.Prefix+"/journal/events?trace=1&limit=1&resource=a%2Fb"); rec.Code != http.StatusOK {
			k.Fatalf("GET journal/events with valid filters = %d %s, want 200", rec.Code, rec.Body)
		}
	})
}
