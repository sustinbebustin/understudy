package parity

import (
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/google/go-cmp/cmp"
)

// fakeTransport answers a scenario's requests in memory and logs what the
// run asked of it, in order.
type fakeTransport struct {
	// answer gives understudy's response to a step request and the
	// operation it was routed to.
	answer func(Outgoing) (Response, string)
	// setup answers a setup call; nil answers 200 {}.
	setup  func(svc, op string, input []byte) Response
	log    []string
	sent   []Outgoing
	inputs [][]byte
}

func (f *fakeTransport) Send(req Outgoing) (Response, string, error) {
	f.sent = append(f.sent, req)
	f.log = append(f.log, "send "+requestLabel(req))
	r, op := f.answer(req)
	return r, op, nil
}

func (f *fakeTransport) Call(svc, op string, input []byte) (Response, error) {
	f.log = append(f.log, "call "+svc+"."+op)
	f.inputs = append(f.inputs, input)
	if f.setup == nil {
		return kmsJSON(200, `{}`), nil
	}
	return f.setup(svc, op, input), nil
}

func (f *fakeTransport) Advance(d time.Duration) error {
	f.log = append(f.log, "advance "+d.String())
	return nil
}

// requestLabel names a request by its awsJson target or awsQuery action.
func requestLabel(req Outgoing) string {
	for _, h := range req.Header {
		if strings.EqualFold(h.Name, "X-Amz-Target") {
			return h.Value
		}
	}
	if q, err := url.ParseQuery(string(req.Body)); err == nil && q.Get("Action") != "" {
		return q.Get("Action")
	}
	return req.Method + " " + req.Target
}

// indexOf is the position of entry in the transport's log, or -1.
func (f *fakeTransport) indexOf(entry string) int { return slices.Index(f.log, entry) }

func kmsJSON(status int, body string) Response {
	return withType("application/x-amz-json-1.1", status, body)
}

// The recorded KMS-alias answer (testdata/parity/kms/KMS-alias/01-duplicate.json).
const kmsDuplicateBody = `{"__type":"AlreadyExistsException","message":"An alias with the name arn:aws:kms:us-east-2:123456789012:alias/usr-rec-20261014-dup already exists"}`

// mapFS holds a scenario file at <id>/scenario.json beside the named
// recorded fixtures and the extra synthetic files.
func mapFS(tb testing.TB, id, scenario string, recorded []string, extra map[string][]byte) fstest.MapFS {
	tb.Helper()
	fsys := fstest.MapFS{id + "/scenario.json": {Data: []byte(scenario)}}
	for _, p := range recorded {
		fsys[p] = &fstest.MapFile{Data: readRecorded(tb, p)}
	}
	for p, b := range extra {
		fsys[p] = &fstest.MapFile{Data: b}
	}
	return fsys
}

func run(tb testing.TB, fsys fstest.MapFS, id string, tr Transport, opts Options) Result {
	tb.Helper()
	res, err := Run(fsys, id, tr, opts)
	if err != nil {
		tb.Fatalf("Run(%s): %v", id, err)
	}
	return res
}

// header is a fixture header pair.
type header = [2]string

// fixtureFile writes a fixture in the recorded format.
func fixtureFile(tb testing.TB, step, client string, exchanges ...map[string]any) []byte {
	tb.Helper()
	b, err := json.Marshal(map[string]any{"step": step, "client": client, "exchanges": exchanges})
	if err != nil {
		tb.Fatal(err)
	}
	return b
}

func kmsRequest(target, body string) map[string]any {
	return map[string]any{
		"method": "POST",
		"url":    "https://kms.us-east-2.amazonaws.com/",
		"headers": []header{
			{"X-Amz-Target", "TrentService." + target},
			{"Content-Type", "application/x-amz-json-1.1"},
			{"X-Amz-Date", "20261014T064028Z"},
			{"Content-Length", strconv.Itoa(len(body))},
		},
		"body": body,
	}
}

func jsonResponse(status int, body string) map[string]any {
	return map[string]any{
		"status":  status,
		"headers": []header{{"Content-Type", "application/x-amz-json-1.1"}},
		"body":    body,
	}
}

func exchange(req, resp map[string]any) map[string]any {
	x := map[string]any{}
	if req != nil {
		x["request"] = req
	}
	if resp != nil {
		x["response"] = resp
	}
	return x
}

const kmsAliasScenario = `{
  "version": 1, "item": "KMS-alias", "account": "123456789012", "region": "us-east-2", "client": "aws-cli",
  "steps": [
    {"fixture": "kms/KMS-alias/01-duplicate.json", "compare": "all"},
    {"fixture": "kms/KMS-alias/02-duplicate-raw.json", "compare": "all"}
  ],
  "settles": [
    {"overlay": "com.amazonaws.kms#AlreadyExistsException", "covers": ["dev.understudy#statusOverride"], "step": "01-duplicate", "field": "status"},
    {"overlay": "com.amazonaws.kms#AlreadyExistsException", "covers": ["dev.understudy#statusOverride"], "step": "02-duplicate-raw", "field": "status"}
  ]
}`

func overlayClaim(step string) Claim {
	return Claim{
		Target: OverlayTarget{Shape: "com.amazonaws.kms#AlreadyExistsException", Covers: []string{"dev.understudy#statusOverride"}},
		Step:   step,
		Field:  "status",
	}
}

// A step answered as AWS answered matches and settles its claim; a step
// whose request body was not recorded is not sent and settles nothing
// (testdata/parity/kms/KMS-alias/02-duplicate-raw.json: body_unrecorded).
func TestRunReplaysTheKMSAliasRecording(t *testing.T) {
	t.Parallel()
	fsys := mapFS(t, "kms/KMS-alias", kmsAliasScenario, []string{kmsDuplicate, kmsDuplicateRaw}, nil)
	tr := &fakeTransport{answer: func(Outgoing) (Response, string) { return kmsJSON(400, kmsDuplicateBody), "kms.CreateAlias" }}
	got := run(t, fsys, "kms/KMS-alias", tr, Options{})
	want := Result{
		Scenario: "kms/KMS-alias",
		Steps: []StepResult{
			{Step: "01-duplicate", Fixture: kmsDuplicate, Operation: "kms.CreateAlias", Outcome: Match{}},
			{Step: "02-duplicate-raw", Fixture: kmsDuplicateRaw, Outcome: Reconstructed{}},
		},
		Claims: []ClaimResult{
			{Claim: overlayClaim("01-duplicate"), State: Settled{}},
			{Claim: overlayClaim("02-duplicate-raw"), State: Open{}},
		},
	}
	if diff := cmp.Diff(want, got, equateEmpty); diff != "" {
		t.Errorf("Run (-want +got):\n%s", diff)
	}
	if len(tr.sent) != 1 {
		t.Fatalf("sent %d requests; want 1, the reconstructed step unsent", len(tr.sent))
	}
	req := tr.sent[0]
	if req.Method != http.MethodPost || req.Target != "/" || req.SigningName != "kms" || req.Region != "us-east-2" {
		t.Errorf("sent %s %s signed for %s in %s; want POST / signed for kms in us-east-2", req.Method, req.Target, req.SigningName, req.Region)
	}
	// A fresh signature replaces Authorization, X-Amz-Date and the length.
	names := make([]string, 0, len(req.Header))
	for _, h := range req.Header {
		names = append(names, h.Name)
	}
	if want := []string{"X-Amz-Target", "Content-Type", "User-Agent", "amz-sdk-invocation-id", "amz-sdk-request"}; !slices.Equal(names, want) {
		t.Errorf("sent headers %v; want %v", names, want)
	}
	var sentBody, recordedBody map[string]string
	if err := json.Unmarshal(req.Body, &sentBody); err != nil {
		t.Fatalf("sent body %q: %v", req.Body, err)
	}
	if err := json.Unmarshal([]byte(`{"AliasName": "alias/usr-rec-20261014-dup", "TargetKeyId": "157cb509-e842-4095-b0b6-e97188e0a507"}`), &recordedBody); err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(recordedBody, sentBody); diff != "" {
		t.Errorf("sent body (-recorded +sent):\n%s", diff)
	}
}

// A step that differs is a named finding, and contradicts what it settles.
func TestRunContradictsAClaimWhoseStepDiffers(t *testing.T) {
	t.Parallel()
	fsys := mapFS(t, "kms/KMS-alias", kmsAliasScenario, []string{kmsDuplicate, kmsDuplicateRaw}, nil)
	tr := &fakeTransport{answer: func(Outgoing) (Response, string) {
		return kmsJSON(400, `{"__type":"AlreadyExistsException","message":"Alias alias/usr-rec-20261014-dup already exists"}`), "kms.CreateAlias"
	}}
	got := run(t, fsys, "kms/KMS-alias", tr, Options{})
	if diff := cmp.Diff(Outcome(Differs{Reason: "error/message, body/message"}), got.Steps[0].Outcome); diff != "" {
		t.Errorf("01-duplicate outcome (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string{"error/message changed", "body/message changed"}, summary(got.Steps[0].Differences)); diff != "" {
		t.Errorf("01-duplicate differences (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(ClaimState(Contradicted{}), got.Claims[0].State); diff != "" {
		t.Errorf("01-duplicate claim (-want +got):\n%s", diff)
	}
}

// The sensitivity check: a settled field that a waiver hides settles
// nothing, however well the step matches.
func TestRunSensitivityFailsAWaivedSettledField(t *testing.T) {
	t.Parallel()
	scenario := `{
  "version": 1, "item": "KMS-alias", "account": "123456789012", "region": "us-east-2", "client": "aws-cli",
  "steps": [{"fixture": "kms/KMS-alias/01-duplicate.json", "compare": "all"}],
  "waivers": [{"step": "01-duplicate", "field": "status", "reason": "understudy answers the model's 409"}],
  "settles": [{"overlay": "com.amazonaws.kms#AlreadyExistsException", "covers": ["dev.understudy#statusOverride"], "step": "01-duplicate", "field": "status"}]
}`
	for _, status := range []int{400, 409} {
		fsys := mapFS(t, "kms/KMS-alias", scenario, []string{kmsDuplicate}, nil)
		tr := &fakeTransport{answer: func(Outgoing) (Response, string) { return kmsJSON(status, kmsDuplicateBody), "kms.CreateAlias" }}
		got := run(t, fsys, "kms/KMS-alias", tr, Options{})
		if diff := cmp.Diff(Outcome(Match{}), got.Steps[0].Outcome); diff != "" {
			t.Errorf("status %d: outcome (-want +got):\n%s", status, diff)
		}
		if diff := cmp.Diff(ClaimState(Insensitive{By: HiddenByWaiver}), got.Claims[0].State); diff != "" {
			t.Errorf("status %d: claim (-want +got):\n%s", status, diff)
		}
	}
}

// A settled field that a normalizer masks settles nothing either: any ISO
// timestamp reads <iso-time> (testdata/parity/sts/R15/06-assume-ok.json
// Expiration), while PackedPolicySize is compared and settles.
func TestRunSensitivityFailsANormalizedSettledField(t *testing.T) {
	t.Parallel()
	scenario := `{
  "version": 1, "item": "R15", "account": "123456789012", "region": "us-east-2", "client": "aws-cli",
  "steps": [{"fixture": "sts/R15/06-assume-ok.json", "compare": "all"}],
  "settles": [
    {"switch": "sts.credentials.expiration", "step": "06-assume-ok", "field": "body/AssumeRoleResponse/AssumeRoleResult/Credentials/Expiration"},
    {"switch": "sts.packed-policy-size", "step": "06-assume-ok", "field": "body/AssumeRoleResponse/AssumeRoleResult/PackedPolicySize"}
  ]
}`
	fsys := mapFS(t, "sts/R15", scenario, []string{stsAssumeOK}, nil)
	minted := understudySide(t, "sts-assume-ok-minted")
	tr := &fakeTransport{answer: func(Outgoing) (Response, string) { return minted, "sts.AssumeRole" }}
	got := run(t, fsys, "sts/R15", tr, Options{})
	if diff := cmp.Diff(Outcome(Match{}), got.Steps[0].Outcome); diff != "" {
		t.Errorf("outcome (-want +got):\n%s\ndifferences: %v", diff, got.Steps[0].Differences)
	}
	if got.Steps[0].Operation != "sts.AssumeRole" {
		t.Errorf("operation %q; want sts.AssumeRole", got.Steps[0].Operation)
	}
	if diff := cmp.Diff([]ClaimState{Insensitive{By: HiddenByNormalizer}, Settled{}}, statesOf(got.Claims)); diff != "" {
		t.Errorf("claims (-want +got):\n%s", diff)
	}
	if len(tr.sent) != 1 || tr.sent[0].SigningName != "sts" {
		t.Fatalf("sent %v; want one request signed for sts", tr.sent)
	}
	q, err := url.ParseQuery(string(tr.sent[0].Body))
	if err != nil || q.Get("Action") != "AssumeRole" || q.Get("RoleArn") != "arn:aws:iam::123456789012:role/usr-rec-20261014-trust" {
		t.Errorf("sent body %q (%v); want the recorded AssumeRole form", tr.sent[0].Body, err)
	}
}

// A step's signing names the credential scope the recording signed for,
// which the fixture drops with Authorization: both R11 07 steps reach
// streams.dynamodb.us-east-2.amazonaws.com, one signed for dynamodb and one
// for streams.dynamodb (the R11 capture's Credential=.../dynamodb/... and
// .../streams.dynamodb/...), where the host's first label is "streams".
func TestRunSignsForAStepsRecordedScope(t *testing.T) {
	t.Parallel()
	scenario := `{
  "version": 1, "item": "R11", "account": "123456789012", "region": "us-east-2", "client": "curl",
  "steps": [
    {"fixture": "dynamodb/R11/07-streams-signed-dynamodb.json", "compare": "all", "signing": "dynamodb"},
    {"fixture": "dynamodb/R11/07-streams-signed-streams.dynamodb.json", "compare": "all", "signing": "streams.dynamodb"}
  ]
}`
	fsys := mapFS(t, "dynamodb/R11", scenario, []string{
		"dynamodb/R11/07-streams-signed-dynamodb.json",
		"dynamodb/R11/07-streams-signed-streams.dynamodb.json",
	}, nil)
	tr := &fakeTransport{answer: func(Outgoing) (Response, string) { return withType("application/x-amz-json-1.0", 200, `{}`), "" }}
	run(t, fsys, "dynamodb/R11", tr, Options{})
	got := make([]string, 0, len(tr.sent))
	for _, o := range tr.sent {
		got = append(got, o.SigningName)
	}
	if want := []string{"dynamodb", "streams.dynamodb"}; !slices.Equal(got, want) {
		t.Errorf("signed for %v; want %v", got, want)
	}
}

// Fixtures of a scenario that creates a key, then describes it: the key id
// AWS returned (157cb509-..., kms/KMS-alias/01-duplicate.json TargetKeyId) is
// bound to the one understudy returns.
func bindFixtures(tb testing.TB) map[string][]byte {
	tb.Helper()
	keyMetadata := `{"KeyMetadata":{"KeyId":"` + recordedKeyID + `","Arn":"` + recordedKey + `"}}`
	return map[string][]byte{
		"kms/BIND/02-create.json": fixtureFile(tb, "02-create", "aws-cli",
			exchange(kmsRequest("CreateKey", `{"Description": "d"}`), jsonResponse(200, keyMetadata))),
		"kms/BIND/01-describe.json": fixtureFile(tb, "01-describe", "aws-cli",
			exchange(kmsRequest("DescribeKey", `{"KeyId": "`+recordedKeyID+`"}`), jsonResponse(200, keyMetadata))),
	}
}

// Listed second but named first, so file-name order would run it first.
const bindScenario = `{
  "version": 1, "item": "BIND", "account": "123456789012", "region": "us-east-2", "client": "aws-cli",
  "setup": [{"service": "kms", "operation": "TagResource", "input": {"KeyId": "157cb509-e842-4095-b0b6-e97188e0a507", "Tags": []}, "before": "01-describe"}],
  "steps": [
    {"fixture": "kms/BIND/02-create.json", "compare": "all", "bind": [{"path": "body/KeyMetadata/KeyId", "shape": {"kind": "uuid"}}]},
    {"fixture": "kms/BIND/01-describe.json", "compare": "all", "advance": "6s"}
  ]
}`

// keyAnswer answers CreateKey with created and DescribeKey with the key id
// the request names.
func keyAnswer(tb testing.TB, created string) func(Outgoing) (Response, string) {
	tb.Helper()
	return func(req Outgoing) (Response, string) {
		id := created
		op := "kms.CreateKey"
		if requestLabel(req) == "TrentService.DescribeKey" {
			var in keyRequest
			if err := json.Unmarshal(req.Body, &in); err != nil {
				tb.Errorf("DescribeKey body %q: %v", req.Body, err)
			}
			id, op = in.KeyID, "kms.DescribeKey"
		}
		return kmsJSON(200, `{"KeyMetadata":{"KeyId":"`+id+`","Arn":"arn:aws:kms:us-east-2:123456789012:key/`+id+`"}}`), op
	}
}

func sentKeyID(tb testing.TB, body []byte) string {
	tb.Helper()
	var in keyRequest
	if err := json.Unmarshal(body, &in); err != nil {
		tb.Fatalf("body %q: %v", body, err)
	}
	return in.KeyID
}

// keyRequest is the key id member of a KMS request.
type keyRequest struct {
	KeyID string `json:"KeyId"`
}

func outcomesOf(steps []StepResult) []Outcome {
	out := make([]Outcome, 0, len(steps))
	for _, s := range steps {
		out = append(out, s.Outcome)
	}
	return out
}

func statesOf(claims []ClaimResult) []ClaimState {
	out := make([]ClaimState, 0, len(claims))
	for _, c := range claims {
		out = append(out, c.State)
	}
	return out
}

// Steps run in the listed order; a setup call runs before the step it names;
// the clock advances before its step; a bound value is substituted into
// every later request, setup inputs included, and masked in understudy's
// answers.
func TestRunBindsValuesAcrossStepsInListedOrder(t *testing.T) {
	t.Parallel()
	fsys := mapFS(t, "kms/BIND", bindScenario, nil, bindFixtures(t))
	tr := &fakeTransport{answer: keyAnswer(t, mintedKeyID)}
	got := run(t, fsys, "kms/BIND", tr, Options{})
	want := []StepResult{
		{Step: "02-create", Fixture: "kms/BIND/02-create.json", Operation: "kms.CreateKey", Outcome: Match{}},
		{Step: "01-describe", Fixture: "kms/BIND/01-describe.json", Operation: "kms.DescribeKey", Outcome: Match{}},
	}
	if diff := cmp.Diff(want, got.Steps, equateEmpty); diff != "" {
		t.Errorf("steps (-want +got):\n%s", diff)
	}
	create, call, advance, describe := tr.indexOf("send TrentService.CreateKey"), tr.indexOf("call kms.TagResource"), tr.indexOf("advance 6s"), tr.indexOf("send TrentService.DescribeKey")
	if create < 0 || call < 0 || advance < 0 || describe < 0 || create > call || call > describe || create > advance || advance > describe {
		t.Errorf("log %v; want CreateKey, then the setup call and the advance, then DescribeKey", tr.log)
	}
	if len(tr.inputs) != 1 || sentKeyID(t, tr.inputs[0]) != mintedKeyID {
		t.Errorf("setup inputs %q; want the key id understudy returned, %s", tr.inputs, mintedKeyID)
	}
	if len(tr.sent) != 2 || sentKeyID(t, tr.sent[1].Body) != mintedKeyID {
		t.Errorf("DescribeKey sent %q; want the key id understudy returned, %s", tr.sent, mintedKeyID)
	}
}

// A value that does not have its shape is not bound: the step differs,
// naming the bind, and later requests carry the AWS value.
func TestRunFailsABindOutsideItsShape(t *testing.T) {
	t.Parallel()
	fsys := mapFS(t, "kms/BIND", bindScenario, nil, bindFixtures(t))
	tr := &fakeTransport{answer: keyAnswer(t, "not-a-uuid")}
	got := run(t, fsys, "kms/BIND", tr, Options{})
	d, ok := got.Steps[0].Outcome.(Differs)
	if !ok || !strings.HasSuffix(d.Reason, "bind body/KeyMetadata/KeyId") {
		t.Errorf("02-create outcome %#v; want differs, the reason ending \"bind body/KeyMetadata/KeyId\"", got.Steps[0].Outcome)
	}
	if len(tr.sent) != 2 || sentKeyID(t, tr.sent[1].Body) != recordedKeyID {
		t.Errorf("DescribeKey sent %q; want the recorded key id %s unsubstituted", tr.sent, recordedKeyID)
	}
	if diff := cmp.Diff(Outcome(Match{}), got.Steps[1].Outcome); diff != "" {
		t.Errorf("01-describe outcome (-want +got):\n%s", diff)
	}
}

// A queued step has a rec command and no fixture yet: it is pending and
// settles nothing. Without a rec command the scenario cannot run.
func TestRunMarksAQueuedStepPending(t *testing.T) {
	t.Parallel()
	queued := `{
  "version": 1, "item": "Q", "account": "123456789012", "region": "us-east-2", "client": "aws-cli",
  "steps": [{"fixture": "kms/Q/01-queued.json", "compare": "all"}],
  "settles": [{"switch": "kms.queued", "step": "01-queued", "field": "status"}],
  "record": [{"step": "01-queued", "argv": ["aws", "kms", "list-keys"]}]
}`
	tr := &fakeTransport{answer: func(Outgoing) (Response, string) { return kmsJSON(200, `{}`), "kms.ListKeys" }}
	got := run(t, mapFS(t, "kms/Q", queued, nil, nil), "kms/Q", tr, Options{})
	want := Result{
		Scenario: "kms/Q",
		Steps:    []StepResult{{Step: "01-queued", Fixture: "kms/Q/01-queued.json", Outcome: Pending{}}},
		Claims:   []ClaimResult{{Claim: Claim{Target: SwitchTarget{Name: "kms.queued"}, Step: "01-queued", Field: "status"}, State: Open{}}},
	}
	if diff := cmp.Diff(want, got, equateEmpty); diff != "" {
		t.Errorf("Run (-want +got):\n%s", diff)
	}
	if len(tr.log) != 0 {
		t.Errorf("transport log %v; want nothing sent for a pending step", tr.log)
	}

	unqueued := strings.Replace(queued, `,
  "record": [{"step": "01-queued", "argv": ["aws", "kms", "list-keys"]}]`, "", 1)
	if unqueued == queued {
		t.Fatal("the scenario without a rec command is the queued one; fix the test's edit")
	}
	if _, err := Run(mapFS(t, "kms/Q", unqueued, nil, nil), "kms/Q", tr, Options{}); err == nil {
		t.Error("Run with a missing fixture and no rec command = nil error; want the scenario refused")
	}
}

// A step the client refused before sending has no exchange
// (testdata/parity/dynamodb/R04/03-ttl-empty-name.json); a scenario that
// needs Docker skips its steps and setup when none serves the run; a REST
// request is not routed by this replayer.
func TestRunSkipsWhatItCannotReplay(t *testing.T) {
	t.Parallel()
	const noExchange = "dynamodb/R04/03-ttl-empty-name.json"
	docker := `{
  "version": 1, "item": "KMS-alias", "account": "123456789012", "region": "us-east-2", "client": "aws-cli",
  "needs": ["docker"],
  "setup": [{"service": "kms", "operation": "CreateKey", "input": {}}],
  "steps": [
    {"fixture": "dynamodb/R04/03-ttl-empty-name.json", "compare": "all"},
    {"fixture": "kms/KMS-alias/01-duplicate.json", "compare": "all"}
  ]
}`
	tr := &fakeTransport{answer: func(Outgoing) (Response, string) { return kmsJSON(400, kmsDuplicateBody), "kms.CreateAlias" }}
	got := run(t, mapFS(t, "kms/KMS-alias", docker, []string{noExchange, kmsDuplicate}, nil), "kms/KMS-alias", tr, Options{Docker: false})
	if diff := cmp.Diff([]Outcome{Skipped{Reason: SkipNoExchange}, Skipped{Reason: SkipDocker}}, outcomesOf(got.Steps)); diff != "" {
		t.Errorf("outcomes (-want +got):\n%s", diff)
	}
	if len(tr.log) != 0 {
		t.Errorf("transport log %v; want no setup call and no request", tr.log)
	}

	rest := `{
  "version": 1, "item": "REST", "account": "123456789012", "region": "us-east-2", "client": "aws-cli",
  "steps": [{"fixture": "s3/REST/01-get.json", "compare": "all"}]
}`
	get := fixtureFile(t, "01-get", "aws-cli", exchange(
		map[string]any{"method": "GET", "url": "https://b.s3.us-east-2.amazonaws.com/k", "headers": []header{{"X-Amz-Date", "20261014T064028Z"}}},
		map[string]any{"status": 200, "headers": []header{}, "body": "v"},
	))
	got = run(t, mapFS(t, "s3/REST", rest, nil, map[string][]byte{"s3/REST/01-get.json": get}), "s3/REST", tr, Options{})
	if diff := cmp.Diff(Outcome(Skipped{Reason: SkipRESTRouting}), got.Steps[0].Outcome); diff != "" {
		t.Errorf("REST outcome (-want +got):\n%s", diff)
	}
	if len(tr.sent) != 0 {
		t.Errorf("sent %v; want a REST request left unsent", tr.sent)
	}
}

// A recording incomplete on the request side is not sent; one incomplete on
// the response side is sent but not compared. Neither settles a claim.
func TestRunReconstructsIncompleteRecordings(t *testing.T) {
	t.Parallel()
	listKeys := kmsRequest("ListKeys", `{}`)
	noHost := kmsRequest("ListKeys", `{}`)
	noHost["url"] = "/"
	omitted := kmsRequest("ListKeys", "")
	delete(omitted, "body")
	omitted["body_omitted"] = map[string]any{"bytes": 70000, "sha256": strings.Repeat("0", 64)}
	noStatus := jsonResponse(200, `{}`)
	delete(noStatus, "status")
	unrecorded := jsonResponse(200, "")
	delete(unrecorded, "body")
	unrecorded["body_unrecorded"] = map[string]any{"bytes": nil}
	files := map[string][]byte{
		"kms/INC/1-no-host.json":           fixtureFile(t, "1-no-host", "http.client", exchange(noHost, jsonResponse(200, `{}`))),
		"kms/INC/2-no-request.json":        fixtureFile(t, "2-no-request", "curl", exchange(nil, jsonResponse(200, `{}`))),
		"kms/INC/3-omitted.json":           fixtureFile(t, "3-omitted", "boto3", exchange(omitted, jsonResponse(200, `{}`))),
		"kms/INC/4-no-status.json":         fixtureFile(t, "4-no-status", "boto3", exchange(listKeys, noStatus)),
		"kms/INC/5-no-response.json":       fixtureFile(t, "5-no-response", "aws-cli", exchange(listKeys, nil)),
		"kms/INC/6-unrecorded-answer.json": fixtureFile(t, "6-unrecorded-answer", "boto3", exchange(listKeys, unrecorded)),
	}
	scenario := `{
  "version": 1, "item": "INC", "account": "123456789012", "region": "us-east-2", "client": "aws-cli",
  "steps": [
    {"fixture": "kms/INC/1-no-host.json", "compare": "all"},
    {"fixture": "kms/INC/2-no-request.json", "compare": "all"},
    {"fixture": "kms/INC/3-omitted.json", "compare": "all"},
    {"fixture": "kms/INC/4-no-status.json", "compare": "all"},
    {"fixture": "kms/INC/5-no-response.json", "compare": "all"},
    {"fixture": "kms/INC/6-unrecorded-answer.json", "compare": "all"}
  ],
  "settles": [
    {"switch": "kms.a", "step": "1-no-host", "field": "status"},
    {"switch": "kms.b", "step": "4-no-status", "field": "body"}
  ]
}`
	tr := &fakeTransport{answer: func(Outgoing) (Response, string) {
		return kmsJSON(500, `{"__type":"KMSInternalException"}`), "kms.ListKeys"
	}}
	got := run(t, mapFS(t, "kms/INC", scenario, nil, files), "kms/INC", tr, Options{})
	wantOps := []string{"", "", "", "kms.ListKeys", "kms.ListKeys", "kms.ListKeys"}
	for i, s := range got.Steps {
		if diff := cmp.Diff(Outcome(Reconstructed{}), s.Outcome); diff != "" {
			t.Errorf("%s outcome (-want +got):\n%s", s.Step, diff)
		}
		if i < len(wantOps) && s.Operation != wantOps[i] {
			t.Errorf("%s operation %q; want %q", s.Step, s.Operation, wantOps[i])
		}
	}
	if len(tr.sent) != 3 {
		t.Errorf("sent %d requests; want 3, the response-side incomplete steps", len(tr.sent))
	}
	for _, c := range got.Claims {
		if diff := cmp.Diff(ClaimState(Open{}), c.State); diff != "" {
			t.Errorf("claim on %s (-want +got):\n%s", c.Claim.Step, diff)
		}
	}
}

// A setup call that fails makes the steps from its point on differ, naming
// the call, and nothing after it runs.
func TestRunStopsAtAFailedSetupCall(t *testing.T) {
	t.Parallel()
	describe := func(step string) []byte {
		return fixtureFile(t, step, "aws-cli", exchange(kmsRequest("DescribeKey", `{}`), jsonResponse(200, `{}`)))
	}
	scenario := `{
  "version": 1, "item": "KMS-alias", "account": "123456789012", "region": "us-east-2", "client": "aws-cli",
  "setup": [{"service": "kms", "operation": "CreateKey", "input": {}, "before": "02-describe"}, {"service": "kms", "operation": "ListKeys", "input": {}}],
  "steps": [
    {"fixture": "kms/KMS-alias/01-duplicate.json", "compare": "all"},
    {"fixture": "kms/KMS-alias/02-describe.json", "compare": "all"},
    {"fixture": "kms/KMS-alias/03-describe.json", "compare": "all"}
  ]
}`
	files := map[string][]byte{"kms/KMS-alias/02-describe.json": describe("02-describe"), "kms/KMS-alias/03-describe.json": describe("03-describe")}
	tr := &fakeTransport{
		answer: func(Outgoing) (Response, string) { return kmsJSON(400, kmsDuplicateBody), "kms.CreateAlias" },
		setup: func(_, op string, _ []byte) Response {
			if op == "CreateKey" {
				return kmsJSON(400, `{"__type":"ValidationException"}`)
			}
			return kmsJSON(200, `{}`)
		},
	}
	got := run(t, mapFS(t, "kms/KMS-alias", scenario, []string{kmsDuplicate}, files), "kms/KMS-alias", tr, Options{})
	failed := Differs{Reason: "setup 0 kms.CreateKey: 400"}
	if diff := cmp.Diff([]Outcome{Match{}, failed, failed}, outcomesOf(got.Steps)); diff != "" {
		t.Errorf("outcomes (-want +got):\n%s", diff)
	}
	if want := []string{"call kms.ListKeys", "send TrentService.CreateAlias", "call kms.CreateKey"}; !slices.Equal(tr.log, want) {
		t.Errorf("transport log %v; want %v", tr.log, want)
	}
}

// Under compare: all every recorded exchange is replayed and an earlier
// one's paths carry exchange/<i>/; under final only the last is
// (lambda/R03/H9-01-create.json holds three 500s before its answer).
func TestRunComparesTheExchangesItsModeSelects(t *testing.T) {
	t.Parallel()
	retried := fixtureFile(t, "01-retried", "aws-cli",
		exchange(kmsRequest("CreateAlias", `{}`), jsonResponse(500, `{"__type":"KMSInternalException","message":"x"}`)),
		exchange(kmsRequest("CreateAlias", `{}`), jsonResponse(400, kmsDuplicateBody)),
	)
	for _, c := range []struct {
		mode    string
		sends   int
		outcome func(Outcome) bool
	}{
		{"all", 2, func(o Outcome) bool {
			d, ok := o.(Differs)
			return ok && strings.HasPrefix(d.Reason, "exchange/0/status") && !strings.Contains(d.Reason, ", status")
		}},
		{"final", 1, func(o Outcome) bool { _, ok := o.(Match); return ok }},
	} {
		scenario := `{"version": 1, "item": "RETRY", "account": "123456789012", "region": "us-east-2", "client": "aws-cli",
  "steps": [{"fixture": "kms/RETRY/01-retried.json", "compare": "` + c.mode + `"}]}`
		tr := &fakeTransport{answer: func(Outgoing) (Response, string) { return kmsJSON(400, kmsDuplicateBody), "kms.CreateAlias" }}
		got := run(t, mapFS(t, "kms/RETRY", scenario, nil, map[string][]byte{"kms/RETRY/01-retried.json": retried}), "kms/RETRY", tr, Options{})
		if len(tr.sent) != c.sends {
			t.Errorf("compare %s: sent %d requests; want %d", c.mode, len(tr.sent), c.sends)
		}
		if !c.outcome(got.Steps[0].Outcome) {
			t.Errorf("compare %s: outcome %#v (differences %v)", c.mode, got.Steps[0].Outcome, got.Steps[0].Differences)
		}
	}
}

// The recorded KMS-alias steps replayed against an in-process understudy:
// AWS's answer and understudy's agree on every compared field
// (testdata/parity/kms/KMS-alias notes: status, __type, message and
// Content-Type match), and the raw curl step, whose body was not kept,
// settles nothing.
func TestReplayKMSAliasThroughTheServer(t *testing.T) {
	t.Parallel()
	sc, err := ReadScenario(recordings(), "kms/KMS-alias")
	if err != nil {
		t.Fatalf("ReadScenario(kms/KMS-alias): %v", err)
	}
	res, err := Run(recordings(), "kms/KMS-alias", ServerTransport(t, sc.Account, "verify"), Options{})
	if err != nil {
		t.Fatalf("Run(kms/KMS-alias): %v", err)
	}
	steps := map[string]StepResult{}
	for _, s := range res.Steps {
		steps[s.Step] = s
	}
	dup, ok := steps["01-duplicate"]
	if !ok {
		t.Fatalf("steps %v; want 01-duplicate", res.Steps)
	}
	if diff := cmp.Diff(Outcome(Match{}), dup.Outcome); diff != "" || dup.Operation != "kms.CreateAlias" {
		t.Errorf("01-duplicate: operation %q, outcome (-want +got):\n%s\ndifferences: %v", dup.Operation, diff, dup.Differences)
	}
	raw, ok := steps["02-duplicate-raw"]
	if !ok {
		t.Fatalf("steps %v; want 02-duplicate-raw", res.Steps)
	}
	if diff := cmp.Diff(Outcome(Reconstructed{}), raw.Outcome); diff != "" || raw.Operation != "" {
		t.Errorf("02-duplicate-raw: operation %q, outcome (-want +got):\n%s", raw.Operation, diff)
	}
	for _, c := range res.Claims {
		if c.Claim.Step != "02-duplicate-raw" {
			continue
		}
		if diff := cmp.Diff(ClaimState(Open{}), c.State); diff != "" {
			t.Errorf("claim %v on the reconstructed step (-want +got):\n%s", c.Claim, diff)
		}
	}
}
