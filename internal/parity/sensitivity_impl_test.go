package parity

import (
	"testing"

	"github.com/google/go-cmp/cmp"
)

// keyFixtures are a CreateKey step whose answer holds the recorded key id
// once (01-create-id), one whose answer also repeats it in the key's ARN
// (02-create-arn), and a DescribeKey of the key (03-describe).
func keyFixtures(tb testing.TB) map[string][]byte {
	tb.Helper()
	idOnly := `{"KeyMetadata":{"KeyId":"` + recordedKeyID + `"}}`
	withArn := `{"KeyMetadata":{"KeyId":"` + recordedKeyID + `","Arn":"` + recordedKey + `"}}`
	return map[string][]byte{
		"kms/SENS/01-create-id.json": fixtureFile(tb, "01-create-id", "aws-cli",
			exchange(kmsRequest("CreateKey", `{"Description": "d"}`), jsonResponse(200, idOnly))),
		"kms/SENS/02-create-arn.json": fixtureFile(tb, "02-create-arn", "aws-cli",
			exchange(kmsRequest("CreateKey", `{"Description": "d"}`), jsonResponse(200, withArn))),
		"kms/SENS/03-describe.json": fixtureFile(tb, "03-describe", "aws-cli",
			exchange(kmsRequest("DescribeKey", `{"KeyId": "`+recordedKeyID+`"}`), jsonResponse(200, withArn))),
	}
}

// The replay the sensitivity check stands for runs the mutated
// recording, so a step's own bind reads the mutated value and binds it:
// a value the step binds, and its answer holds nowhere
// else, is hidden by the bound normalizer and settles nothing. Where the
// answer repeats it (the key id inside the key's ARN), understudy's repeat
// then reads the mutated id while AWS's still reads the recorded one, so the
// comparison fails and the claim settles; so does a later step's use of a
// value an earlier step bound.
func TestRunSensitivityRebindsTheStepsOwnBoundValues(t *testing.T) {
	t.Parallel()
	scenario := `{
  "version": 1, "item": "SENS", "account": "123456789012", "region": "us-east-2", "client": "aws-cli",
  "steps": [
    {"fixture": "kms/SENS/01-create-id.json", "compare": "all", "bind": [{"path": "body/KeyMetadata/KeyId", "shape": {"kind": "uuid"}}]},
    {"fixture": "kms/SENS/02-create-arn.json", "compare": "all", "bind": [{"path": "body/KeyMetadata/KeyId", "shape": {"kind": "uuid"}}]},
    {"fixture": "kms/SENS/03-describe.json", "compare": "all"}
  ],
  "settles": [
    {"switch": "kms.key-id.alone", "step": "01-create-id", "field": "body/KeyMetadata/KeyId"},
    {"switch": "kms.key-id.with-arn", "step": "02-create-arn", "field": "body/KeyMetadata/KeyId"},
    {"switch": "kms.key-arn", "step": "02-create-arn", "field": "body/KeyMetadata/Arn"},
    {"switch": "kms.key-id.described", "step": "03-describe", "field": "body/KeyMetadata/KeyId"}
  ]
}`
	answers := []Response{
		kmsJSON(200, `{"KeyMetadata":{"KeyId":"`+mintedKeyID+`"}}`),
		kmsJSON(200, `{"KeyMetadata":{"KeyId":"`+mintedKeyID+`","Arn":"`+mintedKey+`"}}`),
	}
	tr := &fakeTransport{answer: func(req Outgoing) (Response, string) {
		if requestLabel(req) == "TrentService.DescribeKey" {
			return kmsJSON(200, `{"KeyMetadata":{"KeyId":"`+mintedKeyID+`","Arn":"`+mintedKey+`"}}`), "kms.DescribeKey"
		}
		a := answers[0]
		answers = answers[1:]
		return a, "kms.CreateKey"
	}}
	got := run(t, mapFS(t, "kms/SENS", scenario, nil, keyFixtures(t)), "kms/SENS", tr, Options{})
	if diff := cmp.Diff([]Outcome{Match{}, Match{}, Match{}}, outcomesOf(got.Steps)); diff != "" {
		t.Fatalf("outcomes (-want +got):\n%s\nsteps: %+v", diff, got.Steps)
	}
	want := []ClaimState{Insensitive{By: HiddenByNormalizer}, Settled{}, Settled{}, Settled{}}
	if diff := cmp.Diff(want, statesOf(got.Claims)); diff != "" {
		t.Errorf("claims (-want +got):\n%s", diff)
	}
}

// A mutated bound value that no longer has the bind's shape fails the bind
// in the replay the check describes, so the step would differ: the claim
// settles.
func TestRunSensitivitySettlesAMutationTheBindRefuses(t *testing.T) {
	t.Parallel()
	// The ARN shape requires the two names equal; flipping the recorded
	// name breaks that, while the bound normalizer would hide the value.
	scenario := `{
  "version": 1, "item": "SENS", "account": "123456789012", "region": "us-east-2", "client": "aws-cli",
  "steps": [
    {"fixture": "kms/SENS/02-create-arn.json", "compare": "all", "bind": [{"path": "body/KeyMetadata/Arn", "shape": {"kind": "arn", "service": "kms", "resource": "key"}}]}
  ],
  "settles": [{"switch": "kms.key-arn", "step": "02-create-arn", "field": "body/KeyMetadata/Arn"}]
}`
	tr := &fakeTransport{answer: func(Outgoing) (Response, string) {
		return kmsJSON(200, `{"KeyMetadata":{"KeyId":"`+recordedKeyID+`","Arn":"`+recordedKey+`"}}`), "kms.CreateKey"
	}}
	got := run(t, mapFS(t, "kms/SENS", scenario, nil, keyFixtures(t)), "kms/SENS", tr, Options{})
	if diff := cmp.Diff(Outcome(Match{}), got.Steps[0].Outcome); diff != "" {
		t.Fatalf("outcome (-want +got):\n%s\ndifferences: %v", diff, got.Steps[0].Differences)
	}
	if diff := cmp.Diff([]ClaimState{Settled{}}, statesOf(got.Claims)); diff != "" {
		t.Errorf("claims (-want +got):\n%s", diff)
	}
}

// What hides a field is told apart by the mutation's own effect, not by the
// step's other waived differences: a normalized field of a step that also
// has a waived difference elsewhere is hidden by the normalizer, a waived
// field by its waiver, and a field neither covers settles
// (testdata/parity/sts/R15/06-assume-ok.json, answered with a different
// PackedPolicySize).
func TestRunSensitivityNamesWhatHidesTheField(t *testing.T) {
	t.Parallel()
	scenario := `{
  "version": 1, "item": "R15", "account": "123456789012", "region": "us-east-2", "client": "aws-cli",
  "steps": [{"fixture": "sts/R15/06-assume-ok.json", "compare": "all"}],
  "waivers": [{"step": "06-assume-ok", "field": "body/AssumeRoleResponse/AssumeRoleResult/PackedPolicySize", "reason": "understudy packs policies differently"}],
  "settles": [
    {"switch": "sts.credentials.expiration", "step": "06-assume-ok", "field": "body/AssumeRoleResponse/AssumeRoleResult/Credentials/Expiration"},
    {"switch": "sts.packed-policy-size", "step": "06-assume-ok", "field": "body/AssumeRoleResponse/AssumeRoleResult/PackedPolicySize"},
    {"switch": "sts.assumed-role-arn", "step": "06-assume-ok", "field": "body/AssumeRoleResponse/AssumeRoleResult/AssumedRoleUser/Arn"}
  ]
}`
	packed := understudySide(t, "sts-assume-ok-packed-size")
	tr := &fakeTransport{answer: func(Outgoing) (Response, string) { return packed, "sts.AssumeRole" }}
	got := run(t, mapFS(t, "sts/R15", scenario, []string{stsAssumeOK}, nil), "sts/R15", tr, Options{})
	if diff := cmp.Diff(Outcome(Match{}), got.Steps[0].Outcome); diff != "" {
		t.Fatalf("outcome (-want +got):\n%s\ndifferences: %v", diff, got.Steps[0].Differences)
	}
	want := []ClaimState{Insensitive{By: HiddenByNormalizer}, Insensitive{By: HiddenByWaiver}, Settled{}}
	if diff := cmp.Diff(want, statesOf(got.Claims)); diff != "" {
		t.Errorf("claims (-want +got):\n%s", diff)
	}
}
