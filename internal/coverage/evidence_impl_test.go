package coverage_test

import (
	"errors"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/coverage"
	"github.com/sustinbebustin/understudy/internal/evidence"
	"github.com/sustinbebustin/understudy/internal/fidelity"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/protocoltest/gen/jsonrpc10"
)

// The evidence rule the Operations section prints, and the retirement
// list's introduction, word for word.
const (
	evidenceRule = "Evidence is the weakest claim attributed to the operation, and how many of its claims carry that label. " +
		"A claim is an overlay evidence item whose shape is the operation, its input, its output or one of its errors; " +
		"a fidelity switch whose Ops list the operation; or a recorded step that `internal/parity` replays against it. " +
		"Labels, weakest first: contradicted (<fixture>), when the replay differs from AWS; inferred; sdk-source; aws-doc; " +
		"third-party-recorded; aws-recorded, written aws-recorded (<fixture>) when the replay matches AWS and, for a switch " +
		"or overlay entry it settles, fails once the settled field is changed. unassessed: no claim."
	retirementIntro = "Switches and overlay evidence items whose recorded step matches AWS and fails once the settled field " +
		"is changed, while the code still labels them below aws-recorded. Each is ready to retire: fix its label or remove " +
		"the switch, with a test that cites the fixture (docs/recording-kit.md 6)."
)

// r01 settles a switch by a step of the sqs/R01 scenario.
func r01(step string) fidelity.Settles {
	return fidelity.SettledBy{Step: fidelity.StepRef{Scenario: "sqs/R01", Step: step}}
}

// evidenceReport is one service with claims of every source and label:
//
//   - sqs.a-contradicted, an aws-doc switch whose settling step differs;
//   - sqs.m-code, an sdk-source switch no scenario settles;
//   - sqs.z-settled, an inferred switch whose settling step matches;
//   - overlay items on DeleteMessage's input member and on one of its
//     errors, two of them, only the awsQueryError one settled; and one on
//     the service shape, which no operation's shapes hold;
//   - steps routed to DeleteMessage (match), ReceiveMessage (differs, and
//     a reconstructed one, which is no claim) and GetQueueUrl (two
//     matches).
func evidenceReport() coverage.Report {
	return coverage.Report{
		Services: []coverage.Service{{
			Service: "sqs",
			Operations: []coverage.Operation{
				{Name: "DeleteMessage", Status: coverage.Full{}, Shapes: []string{
					"com.amazonaws.sqs#DeleteMessage", "com.amazonaws.sqs#DeleteMessageRequest", "com.amazonaws.sqs#ReceiptHandleIsInvalid",
				}},
				{Name: "GetQueueUrl", Status: coverage.Full{}, Shapes: []string{"com.amazonaws.sqs#GetQueueUrl"}},
				{Name: "ReceiveMessage", Status: coverage.Partial{Note: "no long poll"}, Shapes: []string{
					"com.amazonaws.sqs#ReceiveMessage", "com.amazonaws.sqs#ReceiveMessageRequest",
				}},
				{Name: "SendMessage", Status: coverage.Full{}, Shapes: []string{"com.amazonaws.sqs#SendMessage"}},
				{Name: "ListQueues", Status: coverage.Unbound{}, Shapes: []string{"com.amazonaws.sqs#ListQueues"}},
			},
			Internal: []coverage.Operation{{Name: "Tick", Status: coverage.Full{}}},
		}},
		Switches: []coverage.Switch{
			{
				Name: "sqs.a-contradicted", Value: "on", Label: evidence.AWSDoc, Ref: "r.md#2",
				Ops: []string{"sqs.ReceiveMessage", "sqs.DeleteMessage"}, Settles: r01("03-recv"),
			},
			{
				Name: "sqs.m-code", Value: "3", Label: evidence.SDKSource, Ref: "r.md#3", Record: "R9",
				Ops: []string{"sqs.ReceiveMessage", "sqs.SendMessage"}, Settles: fidelity.NoScenario{Reason: "unrecorded"},
			},
			{
				Name: "sqs.z-settled", Value: "off", Label: evidence.Inferred, Ref: "r.md#1",
				Ops: []string{"sqs.DeleteMessage"}, Settles: r01("02-delete"),
			},
		},
		Overlays: coverage.OverlaysRead{Entries: []coverage.OverlayEntry{
			{File: "sqs.json", Shape: "com.amazonaws.sqs#AmazonSQS", Label: evidence.AWSRecorded, Source: "s4"},
			{File: "sqs.json", Shape: "com.amazonaws.sqs#DeleteMessageRequest$ReceiptHandle", Label: evidence.ThirdPartyRecorded, Source: "s1"},
			{File: "sqs.json", Shape: "com.amazonaws.sqs#ReceiptHandleIsInvalid", Label: evidence.Inferred, Source: "s2", Covers: []string{"smithy.api#httpError"}},
			{File: "sqs.json", Shape: "com.amazonaws.sqs#ReceiptHandleIsInvalid", Label: evidence.AWSDoc, Source: "s3", Covers: []string{"aws.protocols#awsQueryError"}},
		}},
		Parity: coverage.ParityRead{Scenarios: []coverage.ParityScenario{{
			Scenario: "sqs/R01",
			Steps: []coverage.ParityStep{
				{Step: "02-delete", Fixture: "sqs/R01/02-delete.json", Operation: "sqs.DeleteMessage", Outcome: coverage.StepMatch{}},
				{Step: "03-recv", Fixture: "sqs/R01/03-recv.json", Operation: "sqs.ReceiveMessage", Outcome: coverage.StepDiffers{Reason: "body/Messages"}},
				{Step: "04-recv-unrecorded", Fixture: "sqs/R01/04-recv-unrecorded.json", Operation: "sqs.ReceiveMessage", Outcome: coverage.StepReconstructed{}},
				{Step: "05-raw", Fixture: "sqs/R01/05-raw.json", Outcome: coverage.StepSkipped{Reason: "rest-routing"}},
				{Step: "06-url", Fixture: "sqs/R01/06-url.json", Operation: "sqs.GetQueueUrl", Outcome: coverage.StepMatch{}},
				{Step: "07-url", Fixture: "sqs/R01/07-url.json", Operation: "sqs.GetQueueUrl", Outcome: coverage.StepMatch{}},
			},
			Claims: []coverage.ParityClaim{
				{Target: coverage.TargetSwitch{Name: "sqs.z-settled"}, Step: "02-delete", Field: "status", Result: coverage.ClaimSettled{}},
				{Target: coverage.TargetSwitch{Name: "sqs.a-contradicted"}, Step: "03-recv", Field: "body/Messages", Result: coverage.ClaimContradicted{}},
				{
					Target: coverage.TargetOverlay{Shape: "com.amazonaws.sqs#ReceiptHandleIsInvalid", Covers: []string{"aws.protocols#awsQueryError"}},
					Step:   "02-delete", Field: "error/code", Result: coverage.ClaimSettled{},
				},
				{Target: coverage.TargetOverlay{Shape: "com.amazonaws.sqs#AmazonSQS"}, Step: "02-delete", Field: "header/content-type", Result: coverage.ClaimSettled{}},
			},
		}}},
	}
}

// overlayItem is r's overlay item of shape whose covers, joined by commas,
// are covers.
func overlayItem(t *testing.T, r coverage.Report, shape, covers string) coverage.OverlayEntry {
	t.Helper()
	read, ok := r.Overlays.(coverage.OverlaysRead)
	if !ok {
		t.Fatalf("overlays = %#v, want read", r.Overlays)
	}
	for _, e := range read.Entries {
		if e.Shape == shape && strings.Join(e.Covers, ",") == covers {
			return e
		}
	}
	t.Fatalf("no overlay item %s covering %q", shape, covers)
	return coverage.OverlayEntry{}
}

// addClaims appends claims to r's first parity scenario.
func addClaims(t *testing.T, r *coverage.Report, claims ...coverage.ParityClaim) {
	t.Helper()
	read, ok := r.Parity.(coverage.ParityRead)
	if !ok || len(read.Scenarios) == 0 {
		t.Fatalf("parity = %#v, want a read baseline", r.Parity)
	}
	scenarios := slices.Clone(read.Scenarios)
	scenarios[0].Claims = append(slices.Clone(scenarios[0].Claims), claims...)
	r.Parity = coverage.ParityRead{Scenarios: scenarios}
}

func fromOverlay(e coverage.OverlayEntry) coverage.FromOverlay {
	return coverage.FromOverlay{File: e.File, Shape: e.Shape, Covers: e.Covers}
}

// The claims of an operation, in claim order: the switches whose Ops list
// it by name, the overlay items on its shapes (a member's item by the
// shape that holds the member) strongest first, then the steps routed to
// it that were compared. A switch or overlay item a matching step settles
// reads aws-recorded with that step's fixture, one whose step differs reads
// contradicted; the others keep the label the code states. An item settled
// for other traits than its own keeps its label.
func TestClaimsOfAnOperation(t *testing.T) {
	t.Parallel()
	r := evidenceReport()
	queryError := overlayItem(t, r, "com.amazonaws.sqs#ReceiptHandleIsInvalid", "aws.protocols#awsQueryError")
	httpError := overlayItem(t, r, "com.amazonaws.sqs#ReceiptHandleIsInvalid", "smithy.api#httpError")
	member := overlayItem(t, r, "com.amazonaws.sqs#DeleteMessageRequest$ReceiptHandle", "")
	for _, tc := range []struct {
		op   string
		want []coverage.Claim
	}{
		{"DeleteMessage", []coverage.Claim{
			{Source: coverage.FromSwitch{Name: "sqs.a-contradicted"}, Label: coverage.ContradictedLabel{Fixture: "sqs/R01/03-recv.json"}},
			{Source: coverage.FromSwitch{Name: "sqs.z-settled"}, Label: coverage.RecordedLabel{Fixture: "sqs/R01/02-delete.json"}},
			{Source: fromOverlay(member), Label: coverage.CodeLabel{Label: evidence.ThirdPartyRecorded}},
			{Source: fromOverlay(queryError), Label: coverage.RecordedLabel{Fixture: "sqs/R01/02-delete.json"}},
			{Source: fromOverlay(httpError), Label: coverage.CodeLabel{Label: evidence.Inferred}},
			{Source: coverage.FromStep{Scenario: "sqs/R01", Step: "02-delete", Fixture: "sqs/R01/02-delete.json"}, Label: coverage.RecordedLabel{Fixture: "sqs/R01/02-delete.json"}},
		}},
		{"ReceiveMessage", []coverage.Claim{
			{Source: coverage.FromSwitch{Name: "sqs.a-contradicted"}, Label: coverage.ContradictedLabel{Fixture: "sqs/R01/03-recv.json"}},
			{Source: coverage.FromSwitch{Name: "sqs.m-code"}, Label: coverage.CodeLabel{Label: evidence.SDKSource}},
			{Source: coverage.FromStep{Scenario: "sqs/R01", Step: "03-recv", Fixture: "sqs/R01/03-recv.json"}, Label: coverage.ContradictedLabel{Fixture: "sqs/R01/03-recv.json"}},
		}},
		{"ListQueues", nil},
		{"Tick", nil},
		{"NoSuchOperation", nil},
	} {
		if got := r.Claims("sqs", tc.op); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("Claims(sqs, %s) =\n%+v\nwant\n%+v", tc.op, got, tc.want)
		}
	}
}

// Without the parity baseline a switch keeps its code label and no step is
// a claim; without the overlay files no overlay item is.
func TestClaimsWithoutParityOrOverlays(t *testing.T) {
	t.Parallel()
	r := evidenceReport()
	r.Parity, r.Overlays = coverage.ParityNotCarried{}, coverage.OverlaysNotCarried{}
	want := []coverage.Claim{
		{Source: coverage.FromSwitch{Name: "sqs.a-contradicted"}, Label: coverage.CodeLabel{Label: evidence.AWSDoc}},
		{Source: coverage.FromSwitch{Name: "sqs.z-settled"}, Label: coverage.CodeLabel{Label: evidence.Inferred}},
	}
	if got := r.Claims("sqs", "DeleteMessage"); !reflect.DeepEqual(got, want) {
		t.Errorf("Claims(sqs, DeleteMessage) =\n%+v\nwant\n%+v", got, want)
	}
}

// An operation's evidence is its weakest claim, the first of that rank in
// claim order, and how many claims share the rank; a recorded label ranks
// with aws-recorded, a contradicted one below inferred.
func TestOperationEvidenceIsTheWeakestClaim(t *testing.T) {
	t.Parallel()
	r := evidenceReport()
	for _, tc := range []struct {
		op   string
		want coverage.Evidence
		text string
	}{
		{"DeleteMessage", coverage.Evidence{Weakest: coverage.ContradictedLabel{Fixture: "sqs/R01/03-recv.json"}, AtWeakest: 1, Claims: 6}, "contradicted (sqs/R01/03-recv.json), 1 of 6 claims"},
		{"ReceiveMessage", coverage.Evidence{Weakest: coverage.ContradictedLabel{Fixture: "sqs/R01/03-recv.json"}, AtWeakest: 2, Claims: 3}, "contradicted (sqs/R01/03-recv.json), 2 of 3 claims"},
		{"GetQueueUrl", coverage.Evidence{Weakest: coverage.RecordedLabel{Fixture: "sqs/R01/06-url.json"}, AtWeakest: 2, Claims: 2}, "aws-recorded (sqs/R01/06-url.json), 2 of 2 claims"},
		{"SendMessage", coverage.Evidence{Weakest: coverage.CodeLabel{Label: evidence.SDKSource}, AtWeakest: 1, Claims: 1}, "sdk-source, 1 of 1 claim"},
		{"ListQueues", coverage.Evidence{}, "unassessed"},
	} {
		got := r.OperationEvidence("sqs", tc.op)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("OperationEvidence(sqs, %s) = %+v, want %+v", tc.op, got, tc.want)
		}
		if s := got.String(); s != tc.text {
			t.Errorf("OperationEvidence(sqs, %s).String() = %q, want %q", tc.op, s, tc.text)
		}
	}
}

// Every code label ranks above contradicted and in the vocabulary's order,
// weakest first; aws-recorded and a recorded label share the top rank, so
// the first in claim order is the weakest.
func TestOperationEvidenceRanksLabels(t *testing.T) {
	t.Parallel()
	labels := evidence.Labels()
	for i, strong := range labels {
		for _, weak := range labels[i+1:] {
			r := coverage.Report{
				Services: []coverage.Service{{Service: "kms", Operations: []coverage.Operation{{Name: "Encrypt", Status: coverage.Full{}}}}},
				Switches: []coverage.Switch{
					{Name: "kms.a", Label: strong, Ops: []string{"kms.Encrypt"}},
					{Name: "kms.b", Label: weak, Ops: []string{"kms.Encrypt"}},
				},
			}
			want := coverage.Evidence{Weakest: coverage.CodeLabel{Label: weak}, AtWeakest: 1, Claims: 2}
			if got := r.OperationEvidence("kms", "Encrypt"); !reflect.DeepEqual(got, want) {
				t.Errorf("%s then %s: OperationEvidence = %+v, want %+v", strong, weak, got, want)
			}
		}
	}
	r := coverage.Report{
		Services: []coverage.Service{{Service: "kms", Operations: []coverage.Operation{{Name: "Encrypt", Status: coverage.Full{}}}}},
		Switches: []coverage.Switch{{Name: "kms.a", Label: evidence.AWSRecorded, Ops: []string{"kms.Encrypt"}}},
		Parity: coverage.ParityRead{Scenarios: []coverage.ParityScenario{{Scenario: "kms/K", Steps: []coverage.ParityStep{
			{Step: "01", Fixture: "kms/K/01.json", Operation: "kms.Encrypt", Outcome: coverage.StepMatch{}},
		}}}},
	}
	want := coverage.Evidence{Weakest: coverage.CodeLabel{Label: evidence.AWSRecorded}, AtWeakest: 2, Claims: 2}
	if got := r.OperationEvidence("kms", "Encrypt"); !reflect.DeepEqual(got, want) {
		t.Errorf("aws-recorded switch and a matching step: OperationEvidence = %+v, want %+v", got, want)
	}
}

// One contradicted settles entry outweighs a settled one: the recording
// that differs is a finding the page must not hide. An insensitive or open
// claim settles nothing, so the code label stands.
func TestClaimLabelOfSeveralSettlesEntries(t *testing.T) {
	t.Parallel()
	report := func(results ...coverage.ClaimResult) coverage.Report {
		r := coverage.Report{
			Services: []coverage.Service{{Service: "kms", Operations: []coverage.Operation{{Name: "CreateAlias", Status: coverage.Full{}, Shapes: []string{"com.amazonaws.kms#AlreadyExistsException"}}}}},
			Overlays: coverage.OverlaysRead{Entries: []coverage.OverlayEntry{{File: "kms.json", Shape: "com.amazonaws.kms#AlreadyExistsException", Label: evidence.AWSDoc, Source: "s"}}},
		}
		sc := coverage.ParityScenario{Scenario: "kms/KMS-alias"}
		for i, res := range results {
			step := string(rune('a' + i))
			sc.Steps = append(sc.Steps, coverage.ParityStep{Step: step, Fixture: "kms/KMS-alias/" + step + ".json", Outcome: coverage.StepMatch{}})
			sc.Claims = append(sc.Claims, coverage.ParityClaim{Target: coverage.TargetOverlay{Shape: "com.amazonaws.kms#AlreadyExistsException"}, Step: step, Field: "status", Result: res})
		}
		r.Parity = coverage.ParityRead{Scenarios: []coverage.ParityScenario{sc}}
		return r
	}
	for _, tc := range []struct {
		name    string
		results []coverage.ClaimResult
		want    coverage.ClaimLabel
	}{
		{"settled then contradicted", []coverage.ClaimResult{coverage.ClaimSettled{}, coverage.ClaimContradicted{}}, coverage.ContradictedLabel{Fixture: "kms/KMS-alias/b.json"}},
		{"open then settled", []coverage.ClaimResult{coverage.ClaimOpen{}, coverage.ClaimSettled{}}, coverage.RecordedLabel{Fixture: "kms/KMS-alias/b.json"}},
		{"insensitive", []coverage.ClaimResult{coverage.ClaimInsensitive{By: "waiver"}}, coverage.CodeLabel{Label: evidence.AWSDoc}},
		{"open", []coverage.ClaimResult{coverage.ClaimOpen{}}, coverage.CodeLabel{Label: evidence.AWSDoc}},
	} {
		claims := report(tc.results...).Claims("kms", "CreateAlias")
		if len(claims) != 1 || !reflect.DeepEqual(claims[0].Label, tc.want) {
			t.Errorf("%s: claims = %+v, want one labeled %+v", tc.name, claims, tc.want)
		}
	}
}

// An overlay item with no covers backs every trait of its entry, so a
// settles entry naming any trait settles it.
func TestAnItemWithoutCoversIsSettledForAnyTrait(t *testing.T) {
	t.Parallel()
	r := coverage.Report{
		Services: []coverage.Service{{Service: "kms", Operations: []coverage.Operation{{Name: "CreateAlias", Status: coverage.Full{}, Shapes: []string{"com.amazonaws.kms#AlreadyExistsException"}}}}},
		Overlays: coverage.OverlaysRead{Entries: []coverage.OverlayEntry{{File: "kms.json", Shape: "com.amazonaws.kms#AlreadyExistsException", Label: evidence.Inferred, Source: "s"}}},
		Parity: coverage.ParityRead{Scenarios: []coverage.ParityScenario{{
			Scenario: "kms/KMS-alias",
			Steps:    []coverage.ParityStep{{Step: "01", Fixture: "kms/KMS-alias/01.json", Outcome: coverage.StepMatch{}}},
			Claims: []coverage.ParityClaim{{
				Target: coverage.TargetOverlay{Shape: "com.amazonaws.kms#AlreadyExistsException", Covers: []string{"dev.understudy#statusOverride"}},
				Step:   "01", Field: "status", Result: coverage.ClaimSettled{},
			}},
		}}},
	}
	want := []coverage.Retirement{{
		Source: coverage.FromOverlay{File: "kms.json", Shape: "com.amazonaws.kms#AlreadyExistsException"},
		Code:   evidence.Inferred, Fixture: "kms/KMS-alias/01.json", Field: "status",
	}}
	if got := r.PendingRetirement(); !reflect.DeepEqual(got, want) {
		t.Errorf("PendingRetirement = %+v, want %+v", got, want)
	}
}

// Settled by recording, pending retirement: the switches, then the overlay
// items, a matching step settles while their code label is below
// aws-recorded, each with the fixture and the field it settles. An item
// already labeled aws-recorded is not listed.
func TestPendingRetirement(t *testing.T) {
	t.Parallel()
	r := evidenceReport()
	want := []coverage.Retirement{
		{Source: coverage.FromSwitch{Name: "sqs.z-settled"}, Code: evidence.Inferred, Fixture: "sqs/R01/02-delete.json", Field: "status"},
		{
			Source: fromOverlay(overlayItem(t, r, "com.amazonaws.sqs#ReceiptHandleIsInvalid", "aws.protocols#awsQueryError")),
			Code:   evidence.AWSDoc, Fixture: "sqs/R01/02-delete.json", Field: "error/code",
		},
	}
	if got := r.PendingRetirement(); !reflect.DeepEqual(got, want) {
		t.Errorf("PendingRetirement =\n%+v\nwant\n%+v", got, want)
	}
	r.Parity = coverage.ParityNotCarried{}
	if got := r.PendingRetirement(); len(got) != 0 {
		t.Errorf("PendingRetirement without the baseline = %+v, want none", got)
	}
}

// A claim label prints as the page shows it.
func TestClaimLabelText(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		label coverage.ClaimLabel
		want  string
	}{
		{coverage.CodeLabel{Label: evidence.SDKSource}, "sdk-source"},
		{coverage.RecordedLabel{Fixture: "kms/KMS-alias/01-duplicate.json"}, "aws-recorded (kms/KMS-alias/01-duplicate.json)"},
		{coverage.ContradictedLabel{Fixture: "sqs/R01/03-recv-after-6s.json"}, "contradicted (sqs/R01/03-recv-after-6s.json)"},
	} {
		if got := coverage.ClaimLabelText(tc.label); got != tc.want {
			t.Errorf("ClaimLabelText(%+v) = %q, want %q", tc.label, got, tc.want)
		}
	}
}

// The evidence report's switches, claims and overlay shapes agree.
func TestCheckEvidenceAcceptsAgreeingEvidence(t *testing.T) {
	t.Parallel()
	if err := evidenceReport().CheckEvidence(); err != nil {
		t.Errorf("CheckEvidence = %v", err)
	}
}

// CheckEvidence lists every disagreement between the switches, the
// services, the overlay entries and the baseline's claims, each naming
// what disagrees.
func TestCheckEvidenceListsEveryProblem(t *testing.T) {
	t.Parallel()
	r := evidenceReport()
	r.Switches = append(r.Switches,
		coverage.Switch{Name: "sqs.ghost-op", Label: evidence.Inferred, Ops: []string{"sqs.NoSuchOperation"}, Settles: fidelity.NoScenario{Reason: "x"}},
		coverage.Switch{Name: "sqs.no-step", Label: evidence.Inferred, Ops: []string{"sqs.SendMessage"}, Settles: r01("99-missing")},
		coverage.Switch{Name: "sqs.no-claim", Label: evidence.Inferred, Ops: []string{"sqs.SendMessage"}, Settles: r01("06-url")},
		coverage.Switch{Name: "sqs.other-step", Label: evidence.Inferred, Ops: []string{"sqs.SendMessage"}, Settles: r01("06-url")},
	)
	addClaims(t, &r,
		coverage.ParityClaim{Target: coverage.TargetSwitch{Name: "sqs.m-code"}, Step: "02-delete", Field: "status", Result: coverage.ClaimSettled{}},
		coverage.ParityClaim{Target: coverage.TargetSwitch{Name: "sqs.unknown"}, Step: "02-delete", Field: "status", Result: coverage.ClaimSettled{}},
		coverage.ParityClaim{Target: coverage.TargetSwitch{Name: "sqs.other-step"}, Step: "07-url", Field: "status", Result: coverage.ClaimSettled{}},
		coverage.ParityClaim{Target: coverage.TargetOverlay{Shape: "com.amazonaws.sqs#NoEntry"}, Step: "02-delete", Field: "status", Result: coverage.ClaimSettled{}},
	)
	err := r.CheckEvidence()
	var pe *coverage.ParityError
	if !errors.As(err, &pe) {
		t.Fatalf("CheckEvidence = %v, want a *ParityError", err)
	}
	for _, want := range [][]string{
		{"sqs.ghost-op", "sqs.NoSuchOperation"},
		{"sqs.no-step", "sqs/R01#99-missing"},
		{"sqs.no-claim", "sqs/R01#06-url"},
		{"sqs.m-code", "sqs/R01#02-delete"},
		{"sqs.unknown", "sqs/R01#02-delete"},
		{"sqs.other-step", "sqs/R01#07-url", "sqs/R01#06-url"},
		{"com.amazonaws.sqs#NoEntry", "sqs/R01#02-delete"},
	} {
		found := false
		for _, p := range pe.Problems {
			all := true
			for _, w := range want {
				all = all && strings.Contains(p, w)
			}
			found = found || all
		}
		if !found {
			t.Errorf("no problem names all of %q; problems:\n%s", want, strings.Join(pe.Problems, "\n"))
		}
	}
	if len(pe.Problems) != 8 {
		t.Errorf("%d problems, want 8 (sqs.other-step's SettledBy step also has no claim on it):\n%s", len(pe.Problems), strings.Join(pe.Problems, "\n"))
	}
	if !strings.Contains(err.Error(), pe.Problems[0]) || !strings.Contains(err.Error(), pe.Problems[len(pe.Problems)-1]) {
		t.Errorf("Error() = %q does not list every problem", err)
	}
}

// An overlay claim's shape is checked only when the overlays are read; a
// report without the baseline has nothing to check against.
func TestCheckEvidenceNeedsTheBaseline(t *testing.T) {
	t.Parallel()
	r := evidenceReport()
	addClaims(t, &r, coverage.ParityClaim{Target: coverage.TargetOverlay{Shape: "com.amazonaws.sqs#NoEntry"}, Step: "02-delete", Field: "status", Result: coverage.ClaimSettled{}})
	r.Overlays = coverage.OverlaysNotCarried{}
	if err := r.CheckEvidence(); err != nil {
		t.Errorf("CheckEvidence without overlays = %v, want nil", err)
	}
	r.Parity = coverage.ParityNotCarried{}
	var pe *coverage.ParityError
	if err := r.CheckEvidence(); !errors.As(err, &pe) || !strings.Contains(err.Error(), "baseline") {
		t.Errorf("CheckEvidence without the baseline = %v, want a *ParityError saying none was read", err)
	}
}

// With the baseline and the overlays read, the Operations section prints
// the evidence rule and an Evidence column in every operations table; the
// switch table labels each switch by its claim and names its settling
// step, or why none exists; the pending-retirement list follows the
// overlay entries.
func TestMarkdownShowsTheEvidenceColumn(t *testing.T) {
	t.Parallel()
	md := string(evidenceReport().Markdown())
	for _, want := range []string{
		"listed apart.\n\n" + evidenceRule + "\n\n| Service |",
		"| Operation | Status | Evidence | Note |\n|---|---|---|---|\n",
		"| DeleteMessage | full | contradicted (sqs/R01/03-recv.json), 1 of 6 claims |  |",
		"| ReceiveMessage | partial | contradicted (sqs/R01/03-recv.json), 2 of 3 claims | no long poll |",
		"| SendMessage | full | sdk-source, 1 of 1 claim |  |",
		"| ListQueues | unbound | unassessed |  |",
		"Internal operations:\n\n| Operation | Status | Evidence | Note |\n|---|---|---|---|\n| Tick | full | unassessed |  |",
		"the parity scenario step that settles them, or why none does.",
		"| `sqs.a-contradicted` | `on` | contradicted (sqs/R01/03-recv.json) | r.md#2 | sqs/R01#03-recv |",
		"| `sqs.m-code` | `3` | sdk-source | r.md#3 | none: unrecorded |",
		"| `sqs.z-settled` | `off` | aws-recorded (sqs/R01/02-delete.json) | r.md#1 | sqs/R01#02-delete |",
		"\n### Settled by recording, pending retirement\n\n" + retirementIntro + "\n\n" +
			"| Switch or shape | Code label | Fixture | Field |\n|---|---|---|---|\n" +
			"| `sqs.z-settled` | inferred | sqs/R01/02-delete.json | status |\n" +
			"| `com.amazonaws.sqs#ReceiptHandleIsInvalid` | aws-doc | sqs/R01/02-delete.json | error/code |\n",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("Markdown lacks %q:\n%s", want, md)
		}
	}
	if i, j := strings.Index(md, "### Overlay entries"), strings.Index(md, "### Settled by recording"); i < 0 || j < i {
		t.Errorf("the pending-retirement list does not follow the overlay entries")
	}
}

// An empty pending-retirement list says so.
func TestMarkdownWithNothingPendingRetirement(t *testing.T) {
	t.Parallel()
	r := evidenceReport()
	r.Parity = coverage.ParityRead{}
	md := string(r.Markdown())
	if !strings.Contains(md, "### Settled by recording, pending retirement\n\n"+retirementIntro+"\n\nNone.\n") {
		t.Errorf("Markdown lacks the empty pending-retirement list:\n%s", md)
	}
}

// Without the baseline, or without the overlays, the page is as before:
// no evidence rule, column or retirement list, switches labeled by the
// code and settled by their record-backlog item.
func TestMarkdownWithoutTheBaselineOrOverlaysIsUnchanged(t *testing.T) {
	t.Parallel()
	for name, change := range map[string]func(*coverage.Report){
		"no baseline": func(r *coverage.Report) { r.Parity = coverage.ParityNotCarried{} },
		"no overlays": func(r *coverage.Report) { r.Overlays = coverage.OverlaysNotCarried{} },
	} {
		r := evidenceReport()
		change(&r)
		md := string(r.Markdown())
		for _, absent := range []string{"Evidence is the weakest claim", "| Evidence |", "pending retirement", "none: unrecorded", "parity scenario step"} {
			if strings.Contains(md, absent) {
				t.Errorf("%s: Markdown holds %q:\n%s", name, absent, md)
			}
		}
		for _, want := range []string{"| Operation | Status | Note |", "| DeleteMessage | full |  |", "| `sqs.m-code` | `3` | sdk-source | r.md#3 | R9 |", "| `sqs.z-settled` | `off` | inferred | r.md#1 |  |"} {
			if !strings.Contains(md, want) {
				t.Errorf("%s: Markdown lacks %q:\n%s", name, want, md)
			}
		}
	}
}

// Build records each operation's shapes, the ones overlay evidence attaches
// to it by: the operation, its input and output and its errors, sorted
// and once each; and marks the baseline not carried unless one is given.
func TestBuildRecordsOperationShapesAndParity(t *testing.T) {
	t.Parallel()
	var r coverage.Report
	kerneltest.Run(t, func(k *kerneltest.K) {
		r = coverage.Build(coverage.Input{Registry: k.Kernel.Registry(), Bindings: k.Kernel.Bindings()})
	}, kerneltest.Services(jsonrpc10.Service()))
	shapes := map[string][]string{}
	for _, op := range r.Services[0].Operations {
		shapes[op.Name] = op.Shapes
	}
	for op, want := range map[string][]string{
		"GreetingWithErrors": {
			"aws.protocoltests.json10#ComplexError", "aws.protocoltests.json10#FooError", "aws.protocoltests.json10#GreetingWithErrors",
			"aws.protocoltests.json10#GreetingWithErrorsInput", "aws.protocoltests.json10#GreetingWithErrorsOutput",
			"aws.protocoltests.json10#InvalidGreeting", "com.amazon.coral.validate#ValidationException",
		},
		"EndpointOperation": {"aws.protocoltests.json10#EndpointOperation", "com.amazon.coral.validate#ValidationException", "smithy.api#Unit"},
	} {
		if !reflect.DeepEqual(shapes[op], want) {
			t.Errorf("%s shapes = %q, want %q", op, shapes[op], want)
		}
	}
	if _, ok := r.Parity.(coverage.ParityNotCarried); !ok {
		t.Errorf("Build without a baseline: Parity = %#v, want not carried", r.Parity)
	}
	read := coverage.ParityRead{Scenarios: []coverage.ParityScenario{{Scenario: "kms/K"}}}
	if got := coverage.Build(coverage.Input{Parity: read}).Parity; !reflect.DeepEqual(got, coverage.Parity(read)) {
		t.Errorf("Build with a baseline: Parity = %#v, want %#v", got, read)
	}
}

// The evidence report, its baseline and shapes included, survives JSON.
func TestJSONRoundTripWithParity(t *testing.T) {
	t.Parallel()
	r := evidenceReport()
	roundTrip(t, r)
	data, err := r.JSON()
	if err != nil {
		t.Fatal(err)
	}
	got, err := coverage.ParseJSON(data)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Parity, r.Parity) || !reflect.DeepEqual(got.Services[0].Operations[0].Shapes, r.Services[0].Operations[0].Shapes) {
		t.Errorf("read back parity %+v and shapes %q, want %+v and %q", got.Parity, got.Services[0].Operations[0].Shapes, r.Parity, r.Services[0].Operations[0].Shapes)
	}
	built := coverage.Build(coverage.Input{})
	if data, err = built.JSON(); err != nil || !strings.Contains(string(data), `"parity":{"kind":"not-carried"}`) {
		t.Errorf("JSON of a report without the baseline = %s, %v; want parity not-carried", data, err)
	}
}

// parityBaseline is a baseline.json holding every outcome, target and
// result kind.
const parityBaseline = `{
  "version": 1,
  "scenarios": [
    {
      "scenario": "kms/KMS-alias",
      "steps": [
        {"step": "01-duplicate", "fixture": "kms/KMS-alias/01-duplicate.json", "operation": "kms.CreateAlias", "outcome": {"kind": "match"}},
        {"step": "02-duplicate-raw", "fixture": "kms/KMS-alias/02-duplicate-raw.json", "outcome": {"kind": "reconstructed"}},
        {"step": "03-x", "fixture": "kms/KMS-alias/03-x.json", "operation": "kms.CreateAlias", "outcome": {"kind": "differs", "reason": "status, body/__type"}},
        {"step": "04-y", "fixture": "kms/KMS-alias/04-y.json", "outcome": {"kind": "skipped", "reason": "docker"}},
        {"step": "05-z", "fixture": "kms/KMS-alias/05-z.json", "outcome": {"kind": "pending"}}
      ],
      "claims": [
        {"target": {"kind": "overlay", "shape": "com.amazonaws.kms#AlreadyExistsException"}, "step": "01-duplicate", "field": "status", "result": {"kind": "settled"}},
        {"target": {"kind": "overlay", "shape": "com.amazonaws.kms#AlreadyExistsException", "covers": ["dev.understudy#statusOverride"]}, "step": "02-duplicate-raw", "field": "status", "result": {"kind": "open"}},
        {"target": {"kind": "switch", "name": "kms.a"}, "step": "03-x", "field": "error/code", "result": {"kind": "contradicted"}},
        {"target": {"kind": "switch", "name": "kms.b"}, "step": "01-duplicate", "field": "header/content-type", "result": {"kind": "insensitive", "by": "normalizer"}}
      ]
    }
  ]
}
`

// ParseParity reads baseline.json into the report's parity evidence.
func TestParseParityReadsTheBaseline(t *testing.T) {
	t.Parallel()
	got, err := coverage.ParseParity([]byte(parityBaseline))
	if err != nil {
		t.Fatalf("ParseParity = %v", err)
	}
	want := coverage.ParityRead{Scenarios: []coverage.ParityScenario{{
		Scenario: "kms/KMS-alias",
		Steps: []coverage.ParityStep{
			{Step: "01-duplicate", Fixture: "kms/KMS-alias/01-duplicate.json", Operation: "kms.CreateAlias", Outcome: coverage.StepMatch{}},
			{Step: "02-duplicate-raw", Fixture: "kms/KMS-alias/02-duplicate-raw.json", Outcome: coverage.StepReconstructed{}},
			{Step: "03-x", Fixture: "kms/KMS-alias/03-x.json", Operation: "kms.CreateAlias", Outcome: coverage.StepDiffers{Reason: "status, body/__type"}},
			{Step: "04-y", Fixture: "kms/KMS-alias/04-y.json", Outcome: coverage.StepSkipped{Reason: "docker"}},
			{Step: "05-z", Fixture: "kms/KMS-alias/05-z.json", Outcome: coverage.StepPending{}},
		},
		Claims: []coverage.ParityClaim{
			{Target: coverage.TargetOverlay{Shape: "com.amazonaws.kms#AlreadyExistsException"}, Step: "01-duplicate", Field: "status", Result: coverage.ClaimSettled{}},
			{Target: coverage.TargetOverlay{Shape: "com.amazonaws.kms#AlreadyExistsException", Covers: []string{"dev.understudy#statusOverride"}}, Step: "02-duplicate-raw", Field: "status", Result: coverage.ClaimOpen{}},
			{Target: coverage.TargetSwitch{Name: "kms.a"}, Step: "03-x", Field: "error/code", Result: coverage.ClaimContradicted{}},
			{Target: coverage.TargetSwitch{Name: "kms.b"}, Step: "01-duplicate", Field: "header/content-type", Result: coverage.ClaimInsensitive{By: "normalizer"}},
		},
	}}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ParseParity =\n%+v\nwant\n%+v", got, want)
	}
}

// The checked-in baseline reads.
func TestParseParityReadsTheCheckedInBaseline(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile("../parity/baseline.json")
	if err != nil {
		t.Fatal(err)
	}
	got, err := coverage.ParseParity(data)
	if err != nil || len(got.Scenarios) == 0 {
		t.Errorf("ParseParity(internal/parity/baseline.json) = %d scenarios, %v", len(got.Scenarios), err)
	}
}

// A baseline that breaks the format is refused with every problem it has,
// each naming where it is.
func TestParseParityRefusesABrokenBaseline(t *testing.T) {
	t.Parallel()
	edit := func(oldNew ...string) string { return strings.NewReplacer(oldNew...).Replace(parityBaseline) }
	const one = `{"scenario": "kms/K", "steps": [{"step": "a", "fixture": "kms/K/a.json", "outcome": {"kind": "match"}}]}`
	for _, tc := range []struct {
		name string
		doc  string
		want []string
	}{
		{"not JSON", `{"version": 1,`, []string{"not a baseline document"}},
		{"unknown member", edit(`"version": 1,`, `"version": 1, "extra": true,`), []string{"extra"}},
		{"no version", edit(`"version": 1,`, ``), []string{"version"}},
		{"version 2", edit(`"version": 1,`, `"version": 2,`), []string{"version 2"}},
		{"no scenarios", `{"version": 1}`, []string{"scenarios"}},
		{"unknown outcome kind", edit(`{"kind": "pending"}`, `{"kind": "later"}`), []string{"later"}},
		{"reason on a match", edit(`{"kind": "match"}`, `{"kind": "match", "reason": "x"}`), []string{"reason"}},
		{"by on a settled claim", edit(`{"kind": "settled"}`, `{"kind": "settled", "by": "waiver"}`), []string{"by"}},
		{
			"several problems",
			edit(
				`"scenario": "kms/KMS-alias"`, `"scenario": "kms-alias"`,
				`{"kind": "differs", "reason": "status, body/__type"}`, `{"kind": "differs", "reason": ""}`,
				`{"kind": "skipped", "reason": "docker"}`, `{"kind": "skipped", "reason": "later"}`,
				`"step": "05-z"`, `"step": "04-y"`,
				`"step": "03-x", "field": "error/code"`, `"step": "09-none", "field": "error/code"`,
				`{"kind": "insensitive", "by": "normalizer"}`, `{"kind": "insensitive", "by": "luck"}`,
			),
			[]string{
				`scenarios[0].scenario "kms-alias"`, "scenarios[0].steps[2].outcome", "scenarios[0].steps[3].outcome",
				`scenarios[0].steps[4].step "04-y"`, `scenarios[0].claims[2].step "09-none"`, "scenarios[0].claims[3].result",
			},
		},
		{"duplicate scenario", `{"version": 1, "scenarios": [` + one + `, ` + one + `]}`, []string{`"kms/K" is listed twice`}},
		{"bad field", edit(`"field": "error/code"`, `"field": "error"`), []string{`"error"`}},
		{"no steps", `{"version": 1, "scenarios": [{"scenario": "kms/K", "steps": []}]}`, []string{"scenarios[0].steps"}},
		{"bare switch target", edit(`{"kind": "switch", "name": "kms.a"}`, `{"kind": "switch", "name": ""}`), []string{"scenarios[0].claims[2].target"}},
		{"shape without namespace", edit(`"shape": "com.amazonaws.kms#AlreadyExistsException"}`, `"shape": "AlreadyExistsException"}`), []string{"scenarios[0].claims[0].target"}},
	} {
		_, err := coverage.ParseParity([]byte(tc.doc))
		var pe *coverage.ParityError
		if !errors.As(err, &pe) {
			t.Errorf("%s: ParseParity = %v, want a *ParityError", tc.name, err)
			continue
		}
		for _, w := range tc.want {
			if !strings.Contains(err.Error(), w) {
				t.Errorf("%s: %v does not name %q", tc.name, err, w)
			}
		}
		if len(pe.Problems) < len(tc.want) {
			t.Errorf("%s: %d problems, want at least %d: %q", tc.name, len(pe.Problems), len(tc.want), pe.Problems)
		}
	}
}

// Random baselines survive the report's JSON.
func TestJSONRoundTripParityProperty(t *testing.T) {
	t.Parallel()
	outcome := rapid.Custom(func(t *rapid.T) coverage.ParityOutcome {
		switch rapid.IntRange(0, 4).Draw(t, "outcome") {
		case 0:
			return coverage.StepMatch{}
		case 1:
			return coverage.StepDiffers{Reason: rapid.StringMatching(`[a-z/]{1,12}`).Draw(t, "reason")}
		case 2:
			return coverage.StepReconstructed{}
		case 3:
			return coverage.StepSkipped{Reason: rapid.SampledFrom([]string{"docker", "rest-routing", "no-exchange"}).Draw(t, "skip")}
		default:
			return coverage.StepPending{}
		}
	})
	result := rapid.Custom(func(t *rapid.T) coverage.ClaimResult {
		switch rapid.IntRange(0, 3).Draw(t, "result") {
		case 0:
			return coverage.ClaimSettled{}
		case 1:
			return coverage.ClaimInsensitive{By: rapid.SampledFrom([]string{"waiver", "normalizer"}).Draw(t, "by")}
		case 2:
			return coverage.ClaimContradicted{}
		default:
			return coverage.ClaimOpen{}
		}
	})
	rapid.Check(t, func(t *rapid.T) {
		sc := coverage.ParityScenario{Scenario: "sqs/R01"}
		for i := range rapid.IntRange(1, 4).Draw(t, "steps") {
			sc.Steps = append(sc.Steps, coverage.ParityStep{
				Step: string(rune('a' + i)), Fixture: "sqs/R01/x.json",
				Operation: rapid.SampledFrom([]string{"", "sqs.SendMessage"}).Draw(t, "op"), Outcome: outcome.Draw(t, "outcome"),
			})
		}
		for range rapid.IntRange(0, 3).Draw(t, "claims") {
			var target coverage.ClaimTarget = coverage.TargetSwitch{Name: "sqs.s"}
			if rapid.Bool().Draw(t, "overlay") {
				target = coverage.TargetOverlay{Shape: "com.amazonaws.sqs#X", Covers: rapid.SliceOfN(rapid.StringMatching(`[a-z]{1,5}#[a-z]{1,5}`), 0, 2).Draw(t, "covers")}
			}
			sc.Claims = append(sc.Claims, coverage.ParityClaim{Target: target, Step: "a", Field: "status", Result: result.Draw(t, "result")})
		}
		roundTrip(t, coverage.Report{Overlays: coverage.OverlaysNotCarried{}, Parity: coverage.ParityRead{Scenarios: []coverage.ParityScenario{sc}}})
	})
}
