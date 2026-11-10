package ssm_test

import (
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/ssmapi"
)

// The golden cases replay LocalStack's AWS-validated SSM snapshots
// (third-party-recorded, ssm-parameters.md 0 [R]). Each cites
// localstack@8b9a79f <path>:L<n> and the test's last_validated_date from
// the sibling *.validation.json. They compare at the parsed level: the
// snapshots mask names, dates and the account, so only the members, their
// presence and the literal values settle anything.

// wantParameter compares a Parameter with a recorded one of a first
// version: the members ARN, DataType text, LastModifiedDate, Name, Type
// String, Value and Version 1, and no Selector or SourceResult.
func wantParameter(t *testing.T, what string, got ssmapi.Parameter, name, arn, value string) {
	t.Helper()
	switch {
	case val(got.Name) != name || val(got.ARN) != arn:
		t.Errorf("%s: Name %q ARN %q, want %q %q", what, val(got.Name), val(got.ARN), name, arn)
	case val(got.DataType) != "text" || val(got.Type) != ssmapi.ParameterTypeString || val(got.Value) != value || got.Version != 1:
		t.Errorf("%s: DataType %q Type %q Value %q Version %d, want text String %q 1", what, val(got.DataType), val(got.Type), val(got.Value), got.Version, value)
	case got.LastModifiedDate == nil:
		t.Errorf("%s: no LastModifiedDate", what)
	case got.Selector != nil || got.SourceResult != nil:
		t.Errorf("%s: Selector %v SourceResult %v, want neither", what, got.Selector, got.SourceResult)
	}
}

// G1: GetParameter by the ARN of a root name answers the bare name, the
// ARN parameter/<name>, DataType text, and no Selector or SourceResult
// (localstack@8b9a79f tests/aws/services/ssm/test_ssm.snapshot.json:L2-L15,
// last_validated_date 2024-07-16; request test_ssm.py:L154-L161).
func TestGoldenG1GetParameterByARNOfARootName(t *testing.T) {
	run(t, func(h *harness) {
		const name = "param-g1"
		h.put(name, "test")
		byName := h.mustGet(name, false)
		byARN := h.mustGet(val(byName.ARN), false)
		wantParameter(h.T, "get-parameter by ARN", byARN, name, arnPrefix+name, "test")
	})
}

// G2-G6: test_parameters_with_path (localstack@8b9a79f
// tests/aws/services/ssm/test_ssm.snapshot.json:L16-L94,
// last_validated_date 2025-01-08; requests test_ssm.py:L241-L266) on a
// hierarchical name: the put answers {Tier: Standard, Version: 1} (L19-L26);
// GetParameter by name (L27-L41) and by ARN (L42-L56) answer the name with
// its slash and the ARN parameter<name>; GetParameters by name (L57-L74) and
// by ARN (L75-L92) answer the same entry and InvalidParameters: [].
func TestGoldenG2ToG6ParametersWithPath(t *testing.T) {
	run(t, func(h *harness) {
		const name = "/test/param-g2"
		arn := "arn:aws:ssm:us-east-1:000000000000:parameter" + name

		put := must(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{Name: name, Description: new("test"), Value: "123", Type: new(ssmapi.ParameterTypeString)})
		if val(put.Tier) != ssmapi.ParameterTierStandard || put.Version != 1 {
			h.Errorf("G2 put-parameter-response = Tier %q Version %d, want Standard 1", val(put.Tier), put.Version)
		}

		wantParameter(h.T, "G3 get-parameter-response", h.mustGet(name, false), name, arn, "123")
		wantParameter(h.T, "G4 get-parameter-by-arn-response", h.mustGet(arn, false), name, arn, "123")

		for _, c := range []struct{ what, ask string }{
			{"G5 get-parameters-response", name},
			{"G6 get-parameters-by-arn-response", arn},
		} {
			out := must(h, ssmapi.OpGetParameters, &ssmapi.GetParametersInput{Names: []string{c.ask}})
			if out.InvalidParameters == nil || len(out.InvalidParameters) != 0 {
				h.Errorf("%s: InvalidParameters = %#v, want [] present", c.what, out.InvalidParameters)
			}
			if len(out.Parameters) != 1 {
				h.Errorf("%s: %d parameters, want 1", c.what, len(out.Parameters))
				continue
			}
			wantParameter(h.T, c.what, out.Parameters[0], name, arn, "123")
		}
	})
}

// G7: GetParameter of a deleted parameter answers ParameterNotFound, 400,
// with an empty message (localstack@8b9a79f
// tests/aws/services/cloudformation/resources/test_ssm.snapshot.json:L105-L114,
// last_validated_date 2024-07-03), and botocore prints nothing after the
// colon (localstack@8b9a79f
// tests/aws/services/cloudformation/resource_providers/ssm/test_parameter.snapshot.json:L69,
// last_validated_date 2023-06-21). A plain rule, not a switch.
func TestGoldenG7GetParameterAfterDelete(t *testing.T) {
	run(t, func(h *harness) {
		h.put("/cfn/g7", "v")
		must(h, ssmapi.OpDeleteParameter, &ssmapi.DeleteParameterInput{Name: "/cfn/g7"})
		err := h.get("/cfn/g7")
		wantError(h.T, "GetParameter after delete", err, ssmapi.IdentityParameterNotFound(), "")
		if err != nil && err.ID.Status != 400 {
			h.Errorf("ParameterNotFound status = %d, want 400 (ssm-parameters.md 1 finding 2)", err.ID.Status)
		}
		err = h.get("never-created")
		wantError(h.T, "GetParameter of a name never created", err, ssmapi.IdentityParameterNotFound(), "")
	})
}

// A write stamps LastModifiedDate with the emulator clock (invariant 5;
// ssm-parameters.md 12.3): a read reports the instant of the write, not of
// the read.
func TestLastModifiedDateIsTheWriteInstant(t *testing.T) {
	run(t, func(h *harness) {
		h.advance(time.Hour)
		written := h.Clock.Now().Wire()
		h.put("/clock/a", "v")
		h.advance(time.Minute)
		got := h.mustGet("/clock/a", false)
		if got.LastModifiedDate == nil || !got.LastModifiedDate.Equal(written) {
			h.Errorf("LastModifiedDate = %v, want the write instant %v", got.LastModifiedDate, written)
		}
		if d := h.mustDescribeOne("/clock/a"); d.LastModifiedDate == nil || !d.LastModifiedDate.Equal(written) {
			h.Errorf("DescribeParameters LastModifiedDate = %v, want %v", d.LastModifiedDate, written)
		}
	})
}
