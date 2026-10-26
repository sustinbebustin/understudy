package parity

import (
	"bytes"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/sustinbebustin/understudy/internal/normalize"
)

// A bound value is substituted within each decoded part of a request: a
// path segment, a query value, a header, a form value and a JSON string at
// any depth; the parts it does not occur in keep their recorded bytes, and
// the signature's headers are left for the transport.
func TestOutgoingSubstitutesWithinDecodedParts(t *testing.T) {
	t.Parallel()
	rs := []normalize.Replacement{{Old: "a b/c", New: "x y"}}
	form := Request{
		Method: "POST", URL: "https://sqs.us-east-2.amazonaws.com/123456789012/a%20b%2Fc?QueueUrl=a+b%2Fc&Keep=1%2B1",
		Headers: []Header{
			{"Content-Type", "application/x-www-form-urlencoded; charset=utf-8"},
			{"X-Ref", "a b/c"},
			{"Host", "sqs.us-east-2.amazonaws.com"},
			{"Authorization", "AWS4-HMAC-SHA256 ..."},
			{"X-Amz-Date", "20261014T064028Z"},
			{"X-Amz-Security-Token", "t"},
			{"Content-Length", "9"},
		},
		Body: TextBody{Text: "Action=SendMessage&QueueUrl=a%20b%2Fc&MessageBody=1%2B1"},
	}
	got, err := outgoing(form, rs, "us-east-2")
	if err != nil {
		t.Fatal(err)
	}
	want := Outgoing{
		Method: "POST", Target: "/123456789012/x%20y?QueueUrl=x+y&Keep=1%2B1",
		Header: []Header{{"Content-Type", "application/x-www-form-urlencoded; charset=utf-8"}, {"X-Ref", "x y"}},
		Body:   []byte("Action=SendMessage&QueueUrl=x+y&MessageBody=1%2B1"), SigningName: "sqs", Region: "us-east-2",
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("outgoing(form) (-want +got):\n%s", diff)
	}

	const recorded = `{"KeyId": "a b/c", "Tags": [{"Value": "a b/c"}], "Policy": "{\"r\":\"a b/c\"}", "N": 1.50}`
	json := Request{
		Method: "POST", URL: "https://kms.us-east-2.amazonaws.com/", Headers: []Header{{"Content-Type", "application/x-amz-json-1.1"}},
		Body: TextBody{Text: recorded},
	}
	got, err = outgoing(json, rs, "us-east-2")
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"KeyId":"x y","Tags":[{"Value":"x y"}],"Policy":"{\"r\":\"x y\"}","N":1.50}`; string(got.Body) != want {
		t.Errorf("outgoing(json) body = %s; want %s", got.Body, want)
	}
	// Nothing bound occurs in it: the recorded bytes go out unchanged.
	got, err = outgoing(json, []normalize.Replacement{{Old: "zzz", New: "q"}}, "us-east-2")
	if err != nil {
		t.Fatal(err)
	}
	if string(got.Body) != recorded {
		t.Errorf("outgoing(json, unbound) body = %s; want the recorded bytes", got.Body)
	}
}

// A setup bind that fails halts the scenario as a failed call does, naming
// the bind.
func TestRunStopsAtAFailedSetupBind(t *testing.T) {
	t.Parallel()
	scenario := `{
  "version": 1, "item": "KMS-alias", "account": "123456789012", "region": "us-east-2", "client": "aws-cli",
  "setup": [{"service": "kms", "operation": "CreateKey", "input": {}, "bind": [{"path": "body/KeyMetadata/KeyId", "shape": {"kind": "uuid"}, "aws": "157cb509-e842-4095-b0b6-e97188e0a507"}]}],
  "steps": [{"fixture": "kms/KMS-alias/01-duplicate.json", "compare": "all"}]
}`
	tr := &fakeTransport{
		answer: func(Outgoing) (Response, string) { return kmsJSON(400, kmsDuplicateBody), "kms.CreateAlias" },
		setup:  func(string, string, []byte) Response { return kmsJSON(200, `{"KeyMetadata":{"KeyId":"not-a-uuid"}}`) },
	}
	got := run(t, mapFS(t, "kms/KMS-alias", scenario, []string{kmsDuplicate}, nil), "kms/KMS-alias", tr, Options{})
	if diff := cmp.Diff([]Outcome{Differs{Reason: "setup 0 kms.CreateKey: bind body/KeyMetadata/KeyId"}}, outcomesOf(got.Steps)); diff != "" {
		t.Errorf("outcomes (-want +got):\n%s", diff)
	}
	if len(tr.sent) != 0 {
		t.Errorf("sent %d requests; want none after the failed setup", len(tr.sent))
	}
}

// An AWS value bound once cannot be bound to another understudy value: the
// later bind fails and the first binding stands.
func TestRunRefusesToRebindAValue(t *testing.T) {
	t.Parallel()
	files := bindFixtures(t)
	files["kms/BIND/03-create.json"] = bytes.ReplaceAll(files["kms/BIND/02-create.json"], []byte(`"02-create"`), []byte(`"03-create"`))
	scenario := `{
  "version": 1, "item": "BIND", "account": "123456789012", "region": "us-east-2", "client": "aws-cli",
  "steps": [
    {"fixture": "kms/BIND/02-create.json", "compare": "all", "bind": [{"path": "body/KeyMetadata/KeyId", "shape": {"kind": "uuid"}}]},
    {"fixture": "kms/BIND/03-create.json", "compare": "all", "bind": [{"path": "body/KeyMetadata/KeyId", "shape": {"kind": "uuid"}}]},
    {"fixture": "kms/BIND/01-describe.json", "compare": "all"}
  ]
}`
	keys := []string{mintedKeyID, "1f0e0d0c-0b0a-4909-8807-060504030201"}
	tr := &fakeTransport{}
	tr.answer = func(req Outgoing) (Response, string) {
		if requestLabel(req) == "TrentService.CreateKey" {
			id := keys[0]
			keys = keys[1:]
			return kmsJSON(200, `{"KeyMetadata":{"KeyId":"`+id+`","Arn":"arn:aws:kms:us-east-2:123456789012:key/`+id+`"}}`), "kms.CreateKey"
		}
		return keyAnswer(t, "")(req)
	}
	got := run(t, mapFS(t, "kms/BIND", scenario, nil, files), "kms/BIND", tr, Options{})
	d, ok := got.Steps[1].Outcome.(Differs)
	if !ok || !strings.HasSuffix(d.Reason, "bind body/KeyMetadata/KeyId") {
		t.Errorf("03-create outcome %#v; want differs naming the bind", got.Steps[1].Outcome)
	}
	if len(tr.sent) != 3 || sentKeyID(t, tr.sent[2].Body) != mintedKeyID {
		t.Errorf("DescribeKey sent %q; want the first binding's key id %s", tr.sent, mintedKeyID)
	}
}

// regionalTransport records the region Run gives it.
type regionalTransport struct {
	fakeTransport
	regions []string
}

func (r *regionalTransport) InRegion(region string) {
	r.regions = append(r.regions, region)
	r.log = append(r.log, "region "+region)
}

// A transport that signs setup calls learns the scenario's region before
// the first call.
func TestRunTellsARegionalTransportTheRegion(t *testing.T) {
	t.Parallel()
	tr := &regionalTransport{answer: keyAnswer(t, mintedKeyID)}
	run(t, mapFS(t, "kms/BIND", bindScenario, nil, bindFixtures(t)), "kms/BIND", tr, Options{})
	if !slices.Equal(tr.regions, []string{"us-east-2"}) || tr.log[0] != "region us-east-2" {
		t.Errorf("log %v; want the region us-east-2 given once, first", tr.log)
	}
}

// A settles field whose parent the recording does not hold cannot be
// flipped, so the scenario is refused rather than the claim guessed.
func TestRunRefusesASettlesFieldUnderAnAbsentParent(t *testing.T) {
	t.Parallel()
	scenario := strings.Replace(kmsAliasScenario, `"field": "status"}`, `"field": "body/nope/deeper"}`, 1)
	tr := &fakeTransport{answer: func(Outgoing) (Response, string) { return kmsJSON(400, kmsDuplicateBody), "kms.CreateAlias" }}
	_, err := Run(mapFS(t, "kms/KMS-alias", scenario, []string{kmsDuplicate, kmsDuplicateRaw}, nil), "kms/KMS-alias", tr, Options{})
	var se *ScenarioError
	if !errors.As(err, &se) || se.Field != "settles[0].field" {
		t.Errorf("Run error = %v; want a *ScenarioError at settles[0].field", err)
	}
}

// The clock advance and the sends of a step the replayer cannot send do not
// happen: a reconstructed step does not advance the clock.
func TestRunAdvancesOnlyBeforeASentStep(t *testing.T) {
	t.Parallel()
	scenario := `{
  "version": 1, "item": "KMS-alias", "account": "123456789012", "region": "us-east-2", "client": "aws-cli",
  "steps": [
    {"fixture": "kms/KMS-alias/02-duplicate-raw.json", "compare": "all", "advance": "1m"},
    {"fixture": "kms/KMS-alias/01-duplicate.json", "compare": "all", "advance": "6s"}
  ]
}`
	tr := &fakeTransport{answer: func(Outgoing) (Response, string) { return kmsJSON(400, kmsDuplicateBody), "kms.CreateAlias" }}
	run(t, mapFS(t, "kms/KMS-alias", scenario, []string{kmsDuplicate, kmsDuplicateRaw}, nil), "kms/KMS-alias", tr, Options{})
	if want := []string{"advance " + (6 * time.Second).String(), "send TrentService.CreateAlias"}; !slices.Equal(tr.log, want) {
		t.Errorf("log %v; want %v", tr.log, want)
	}
}
