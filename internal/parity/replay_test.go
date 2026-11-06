package parity

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/sustinbebustin/understudy/admin"
	"github.com/sustinbebustin/understudy/internal/api/cloudwatchapi"
	"github.com/sustinbebustin/understudy/internal/api/dynamodbapi"
	"github.com/sustinbebustin/understudy/internal/api/eventbridgeapi"
	"github.com/sustinbebustin/understudy/internal/api/iamapi"
	"github.com/sustinbebustin/understudy/internal/api/kmsapi"
	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/api/logsapi"
	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/api/secretsmanagerapi"
	"github.com/sustinbebustin/understudy/internal/api/sesapi"
	"github.com/sustinbebustin/understudy/internal/api/snsapi"
	"github.com/sustinbebustin/understudy/internal/api/sqsapi"
	"github.com/sustinbebustin/understudy/internal/api/ssmapi"
	"github.com/sustinbebustin/understudy/internal/api/stsapi"
	"github.com/sustinbebustin/understudy/internal/auth"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/server"
)

var updateBaseline = flag.Bool("update-baseline", false, "rewrite internal/parity/baseline.json from this replay (just parity-baseline)")

// The replay's fixed inputs.
const (
	// replayHost is the Host of every replayed request: the in-process
	// handler answers loopback names only (its DNS rebinding defence).
	replayHost = "localhost:4566"
	// replayPeer is the loopback peer every replayed request comes from.
	replayPeer = "127.0.0.1:40000"
	// replaySecret is the dummy secret a 12-digit key signs with (ADR 0012
	// amendment 2026-10-13).
	replaySecret = "test"
)

// replayEpoch is where the replay's emulator clock is frozen: the day the
// recordings were made (testdata/parity/*/*/notes.md, 2026-10-14).
var replayEpoch = time.Date(2026, time.October, 14, 0, 0, 0, 0, time.UTC)

// ServerTransport opens a fresh in-process understudy for one Run under the
// SigV4 mode sigv4 ("parse" or "verify") with the emulator clock frozen.
// It signs as account's root (ADR 0012: a 12-digit key is that account's
// root) with the dummy secret, at the wall time the server's clock reads,
// since verify judges request skew on the wall clock
// (internal/edge/verify.go); encodes setup calls by the registry's protocol
// for the operation; and reads the routed operation from the call's journal
// event. Closed by tb.Cleanup.
func ServerTransport(tb testing.TB, account, sigv4 string) Transport {
	tb.Helper()
	var mode server.SigV4Mode
	switch sigv4 {
	case "parse":
		mode = server.SigV4ParseOnly
	case "verify":
		mode = server.SigV4Verify
	default:
		tb.Fatalf("ServerTransport: sigv4 mode %q is not parse or verify", sigv4)
	}
	seed := uint64(1)
	srv, err := server.New(server.Config{
		ClockStart: server.ClockFrozen{At: replayEpoch},
		Seed:       &seed,
		SigV4:      mode,
		Docker:     server.DockerDisabled{},
		DynamoDB:   server.DynamoDBDisabled{},
		LogOutput:  io.Discard,
		Version:    "parity-replay",
	})
	if err != nil {
		tb.Fatalf("building the in-process understudy for the replay: %v", err)
	}
	tb.Cleanup(func() {
		if err := srv.Close(); err != nil {
			tb.Errorf("closing the replay's in-process understudy: %v", err)
		}
	})
	c, err := admin.New("http://"+replayHost, admin.Options{HTTPClient: &http.Client{Transport: handlerTransport{srv.Handler()}}})
	if err != nil {
		tb.Fatal(err)
	}
	return &serverTransport{tb: tb, handler: srv.Handler(), admin: c, account: account, region: aws.DefaultRegion().String()}
}

// handlerTransport serves an admin client's requests with the in-process
// handler, as a loopback listener would deliver them.
type handlerTransport struct{ h http.Handler }

func (t handlerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	in := req.Clone(req.Context())
	in.RequestURI, in.RemoteAddr = req.URL.RequestURI(), replayPeer
	if in.Host == "" {
		in.Host = req.URL.Host
	}
	if in.Body == nil {
		in.Body = http.NoBody
	}
	rec := httptest.NewRecorder()
	t.h.ServeHTTP(rec, in)
	return rec.Result(), nil
}

// serverTransport is ServerTransport's Transport.
type serverTransport struct {
	tb      testing.TB
	handler http.Handler
	admin   *admin.Client
	account string
	// region is what setup calls sign for: the scenario's (InRegion).
	region string
}

func (s *serverTransport) InRegion(region string) { s.region = region }

func (s *serverTransport) Send(o Outgoing) (Response, string, error) {
	req := httptest.NewRequestWithContext(s.tb.Context(), o.Method, "http://"+replayHost+o.Target, bytes.NewReader(o.Body))
	for _, h := range o.Header {
		req.Header.Add(h.Name, h.Value)
	}
	return s.do(req, o.Body, o.SigningName, o.Region)
}

func (s *serverTransport) Call(svc, op string, input []byte) (Response, error) {
	desc, ok := replayServices()[ref.ServiceID(svc)]
	if !ok {
		return Response{}, fmt.Errorf("setup service %q is not a registry service id, such as kms, iam, sqs or eventbridge", svc)
	}
	o, ok := desc.Operation(op)
	if !ok {
		return Response{}, fmt.Errorf("setup operation %s is not an operation of %s's model", op, svc)
	}
	req, body, err := setupRequest(s.tb, desc, o, input)
	if err != nil {
		return Response{}, fmt.Errorf("encoding setup call %s.%s: %w", svc, op, err)
	}
	resp, _, err := s.do(req, body, desc.SigningName, s.region)
	return resp, err
}

func (s *serverTransport) Advance(d time.Duration) error {
	_, err := s.admin.ChangeClock(s.tb.Context(), admin.Advance{By: d})
	return err
}

// do signs req, serves it and reads the routed operation from its journal
// event.
func (s *serverTransport) do(req *http.Request, body []byte, signing, region string) (Response, string, error) {
	r, err := aws.KnownRegions().Parse(region)
	if err != nil {
		return Response{}, "", err
	}
	clk, err := s.admin.Clock(s.tb.Context())
	if err != nil {
		return Response{}, "", fmt.Errorf("reading the emulator clock to sign with: %w", err)
	}
	req.RemoteAddr = replayPeer
	auth.Sign(req, body, auth.Signer{AccessKeyID: s.account, SecretAccessKey: replaySecret, Region: r, Service: signing}, clk.Wall)
	rec := httptest.NewRecorder()
	s.handler.ServeHTTP(rec, req)
	res := rec.Result()
	resp, err := responseOf(res)
	if err != nil {
		return Response{}, "", err
	}
	op, err := s.routed(res.Header)
	return resp, op, err
}

// routed is the operation the journal's api-call event for the request id
// of an answer names, "" when the answer has no id or no event, as for a
// request the edge refused before routing it.
func (s *serverTransport) routed(h http.Header) (string, error) {
	id := h.Get("X-Amzn-Requestid")
	if id == "" {
		id = h.Get("X-Amz-Request-Id")
	}
	if id == "" {
		return "", nil
	}
	page, err := s.admin.JournalEvents(s.tb.Context(), admin.JournalQuery{RequestID: id, Kind: "api-call"})
	if err != nil {
		return "", fmt.Errorf("reading the journal event of request %s: %w", id, err)
	}
	if len(page.Events) == 0 {
		return "", nil
	}
	var call struct {
		Service   string `json:"service"`
		Operation string `json:"operation"`
	}
	if err := json.Unmarshal(page.Events[0].Detail, &call); err != nil {
		return "", fmt.Errorf("reading the journal event of request %s: %w", id, err)
	}
	return call.Service + "." + call.Operation, nil
}

// responseOf is an answer as a recorded response: headers by name, the body
// as text when it is UTF-8.
func responseOf(res *http.Response) (Response, error) {
	b, err := io.ReadAll(res.Body)
	if err != nil {
		return Response{}, err
	}
	r := Response{Status: res.StatusCode, Body: NoBody{}}
	for _, name := range slices.Sorted(maps.Keys(res.Header)) {
		for _, v := range res.Header[name] {
			r.Headers = append(r.Headers, Header{Name: name, Value: v})
		}
	}
	switch {
	case len(b) == 0:
	case utf8.Valid(b):
		r.Body = TextBody{Text: string(b)}
	default:
		r.Body = BinaryBody{Bytes: b}
	}
	return r, nil
}

// replayServices are the generated services' descriptors by id, the
// registry the server wires (internal/server/wire.go apis).
func replayServices() map[ref.ServiceID]*registry.Service {
	m := map[ref.ServiceID]*registry.Service{}
	for _, s := range []*registry.Service{
		sqsapi.Service(), snsapi.Service(), s3api.Service(), dynamodbapi.Service(), lambdaapi.Service(), logsapi.Service(),
		secretsmanagerapi.Service(), kmsapi.Service(), iamapi.Service(), stsapi.Service(), eventbridgeapi.Service(), cloudwatchapi.Service(),
		ssmapi.Service(), sesapi.Service(),
	} {
		m[s.ID] = s
	}
	return m
}

// setupRequest encodes a setup call's input in its service's RPC protocol:
// awsJson as sent, or awsQuery's Action, Version and the input flattened by
// the awsQuery key rules (lists as .member.N, maps as .entry.N.key/value,
// flattened members without the wrapper, xmlName renames).
func setupRequest(tb testing.TB, desc *registry.Service, op *registry.Operation, input []byte) (*http.Request, []byte, error) {
	tb.Helper()
	var contentType string
	switch {
	case slices.Contains(desc.Protocols, ref.AWSJSON10):
		contentType = "application/x-amz-json-1.0"
	case slices.Contains(desc.Protocols, ref.AWSJSON11):
		contentType = "application/x-amz-json-1.1"
	case slices.Contains(desc.Protocols, ref.AWSQuery):
		contentType = "application/x-www-form-urlencoded; charset=utf-8"
		form, err := queryForm(op, input)
		if err != nil {
			return nil, nil, err
		}
		input = []byte(form)
	default:
		return nil, nil, fmt.Errorf("setup calls reach awsJson and awsQuery operations; %s speaks %v", desc.ID, desc.Protocols)
	}
	req := httptest.NewRequestWithContext(tb.Context(), http.MethodPost, "http://"+replayHost+"/", bytes.NewReader(input))
	req.Header.Set("Content-Type", contentType)
	if !strings.HasPrefix(contentType, "application/x-www-form-urlencoded") {
		req.Header.Set("X-Amz-Target", desc.JSONTargetPrefix+"."+op.ID.Name)
	}
	return req, input, nil
}

func queryForm(op *registry.Operation, input []byte) (string, error) {
	dec := json.NewDecoder(bytes.NewReader(input))
	dec.UseNumber()
	var v map[string]any
	if err := dec.Decode(&v); err != nil {
		return "", err
	}
	pairs := []string{"Action=" + url.QueryEscape(op.ID.Name), "Version=" + url.QueryEscape(op.Service.QueryVersion)}
	return strings.Join(appendQueryStruct(pairs, "", op.Input, v), "&"), checkMembers(op.Input, v)
}

// checkMembers refuses an input member the schema does not have, at any
// depth, which the encoder would otherwise drop, and an object or array
// where the schema has none: a typo in a setup input is an error, not a
// crash of the whole replay.
func checkMembers(s *smithy.Schema, v any) error {
	switch x := v.(type) {
	case map[string]any:
		switch {
		case s == nil:
			return fmt.Errorf("input object %v has no schema to hold it; the operation takes no input there", x)
		case s.Type == smithy.TypeMap && len(s.Members) == 2:
			for _, e := range x {
				if err := checkMembers(s.Members[1].Target, e); err != nil {
					return err
				}
			}
			return nil
		}
		for name, e := range x {
			i := slices.IndexFunc(s.Members, func(m *smithy.Member) bool { return m.Name == name })
			if i < 0 {
				return fmt.Errorf("input member %q is not a member of %v", name, s.ID)
			}
			if err := checkMembers(s.Members[i].Target, e); err != nil {
				return err
			}
		}
	case []any:
		if s == nil || s.Type != smithy.TypeList || len(s.Members) == 0 {
			return fmt.Errorf("input array %v is not a list member of the operation's input", x)
		}
		for _, e := range x {
			if err := checkMembers(s.Members[0].Target, e); err != nil {
				return err
			}
		}
	}
	return nil
}

// A setup input with an object or array where the operation has no schema
// is refused with an error rather than crashing the replay.
func TestCheckMembersRefusesInputWithoutASchema(t *testing.T) {
	t.Parallel()
	for _, v := range []any{map[string]any{"QueueName": "q"}, []any{"q"}} {
		if err := checkMembers(nil, v); err == nil {
			t.Errorf("checkMembers(nil, %v) = nil; want an error", v)
		}
	}
	str := &smithy.Schema{Type: smithy.TypeString}
	if err := checkMembers(str, []any{"q"}); err == nil {
		t.Error("checkMembers(string schema, array) = nil; want an error")
	}
}

func appendQueryStruct(pairs []string, prefix string, s *smithy.Schema, v map[string]any) []string {
	if s == nil {
		return pairs
	}
	for _, m := range s.Members {
		if x, ok := v[m.Name]; ok {
			pairs = appendQueryValue(pairs, joinKey(prefix, queryKey(m, m.Name)), m, true, x)
		}
	}
	return pairs
}

func appendQueryValue(pairs []string, key string, m *smithy.Member, inStruct bool, v any) []string {
	flat := inStruct && m.XMLFlattened
	switch x := v.(type) {
	case map[string]any:
		if m.Target.Type != smithy.TypeMap {
			return appendQueryStruct(pairs, key, m.Target, x)
		}
		base := key
		if !flat {
			base += ".entry"
		}
		km, vm := m.Target.Members[0], m.Target.Members[1]
		for i, k := range slices.Sorted(maps.Keys(x)) {
			entry := base + "." + strconv.Itoa(i+1)
			pairs = append(pairs, url.QueryEscape(entry+"."+queryKey(km, "key"))+"="+url.QueryEscape(k))
			pairs = appendQueryValue(pairs, entry+"."+queryKey(vm, "value"), vm, false, x[k])
		}
		return pairs
	case []any:
		if len(x) == 0 {
			return append(pairs, url.QueryEscape(key)+"=")
		}
		elem := m.Target.Members[0]
		base := key
		if !flat {
			base += "." + queryKey(elem, "member")
		}
		for i, e := range x {
			pairs = appendQueryValue(pairs, base+"."+strconv.Itoa(i+1), elem, false, e)
		}
		return pairs
	case string:
		return append(pairs, url.QueryEscape(key)+"="+url.QueryEscape(x))
	case json.Number:
		return append(pairs, url.QueryEscape(key)+"="+x.String())
	case bool:
		return append(pairs, url.QueryEscape(key)+"="+strconv.FormatBool(x))
	}
	return pairs
}

func queryKey(m *smithy.Member, fallback string) string {
	if m.XMLName != "" {
		return m.XMLName
	}
	return fallback
}

func joinKey(prefix, key string) string {
	if prefix == "" {
		return key
	}
	return prefix + "." + key
}

// replayCorpus replays every scenario under testdata/parity once per SigV4
// mode of modes, each against its own in-process understudy, with no Docker
// daemon, and returns one corpus of results per mode, in modes order. The
// replays run concurrently: a clock advance answers only once the next wall
// second begins (internal/edge/timeline.go, settledWriter), so a scenario
// with many advances takes seconds of wall time on its own.
func replayCorpus(t *testing.T, modes ...string) [][]Result {
	t.Helper()
	fsys := recordings()
	ids, err := ListScenarios(fsys)
	if err != nil {
		t.Fatal(err)
	}
	type replay struct {
		id   string
		mode int // index into modes
		t    Transport
		res  Result
		err  error
	}
	replays := make([]*replay, 0, len(ids)*len(modes))
	for _, id := range ids {
		sc, err := ReadScenario(fsys, id)
		if err != nil {
			t.Fatalf("ReadScenario(%s): %v", id, err)
		}
		for m, mode := range modes {
			// Built here, on the test goroutine, since ServerTransport may
			// stop the test.
			replays = append(replays, &replay{id: id, mode: m, t: ServerTransport(t, sc.Account, mode)})
		}
	}
	var wg sync.WaitGroup
	for _, r := range replays {
		wg.Go(func() { r.res, r.err = Run(fsys, r.id, r.t, Options{Docker: false}) })
	}
	wg.Wait()
	corpora := make([][]Result, len(modes))
	for _, r := range replays {
		if r.err != nil {
			t.Fatalf("Run(%s) under sigv4 %s: %v", r.id, modes[r.mode], r.err)
		}
		corpora[r.mode] = append(corpora[r.mode], r.res)
	}
	return corpora
}

// The ratchet: every scenario replays as baseline.json records, step by
// step and claim by claim. -update-baseline (just parity-baseline) rewrites
// the file instead.
func TestReplay(t *testing.T) {
	t.Parallel()
	results := replayCorpus(t, "verify")[0]
	got := BaselineOf(results)
	if *updateBaseline {
		enc, err := got.Encode()
		if err != nil {
			t.Fatalf("encoding the replay's baseline: %v", err)
		}
		if err := os.WriteFile("baseline.json", enc, 0o600); err != nil {
			t.Fatalf("writing internal/parity/baseline.json: %v", err)
		}
		return
	}
	for _, d := range Ratchet(checkedInBaseline(t), got) {
		t.Errorf("%s%s\nRun just parity-baseline once the change is intended; a pending step that now differs is a finding: fix understudy or the research (CLAUDE.md invariant 2)", d, stepDifferences(results, d.Key))
	}
}

// checkedInBaseline is internal/parity/baseline.json.
func checkedInBaseline(t *testing.T) Baseline {
	t.Helper()
	data, err := os.ReadFile("baseline.json")
	if err != nil {
		t.Fatalf("reading internal/parity/baseline.json: %v; run just parity-baseline", err)
	}
	b, err := ParseBaseline(data)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// stepDifferences lists the differences of the Differs step a drift key
// names, one per line, for the failure message.
func stepDifferences(results []Result, key string) string {
	var b strings.Builder
	for _, r := range results {
		for _, s := range r.Steps {
			if r.Scenario+"#"+s.Step != key {
				continue
			}
			for _, d := range s.Differences {
				b.WriteString("\n  " + d.String())
			}
		}
	}
	return b.String()
}

// Signatures are checked under verify and only read under parse; the
// replay signs correctly, so both modes replay alike. TestReplay holds the
// verify replay to baseline.json, so this one holds the parse replay to the
// same file rather than replaying verify a second time. Under
// -update-baseline TestReplay rewrites the file, so there is nothing yet to
// hold this one to.
func TestReplaySigV4Parse(t *testing.T) {
	t.Parallel()
	if *updateBaseline {
		t.Skip("TestReplay is rewriting baseline.json from the verify replay")
	}
	parse := BaselineOf(replayCorpus(t, "parse")[0])
	for _, d := range Ratchet(checkedInBaseline(t), parse) {
		t.Errorf("under sigv4 parse, %s", d)
	}
}
