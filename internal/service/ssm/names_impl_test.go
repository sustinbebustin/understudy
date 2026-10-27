package ssm_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/ssmapi"
)

// Name choices no AWS evidence fixes yet (UNVERIFIED SSM-04,
// ssm-parameters.md 3.1): a trailing slash and an empty name are refused,
// the prefix test reads the first level literally so awsome is refused, and
// a name under an aws/ssm-free first level with aws further down is fine.
func TestNameChoices(t *testing.T) {
	run(t, func(h *harness) {
		for _, name := range []string{"/a/b/", "/", "   ", "awsome", "/SsMx/y"} {
			_, err := call(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{Name: name, Value: "v", Type: new(ssmapi.ParameterTypeString)})
			wantCode(h.T, fmt.Sprintf("PutParameter %q", name), err, ssmapi.IdentityValidationException())
		}
		h.put("/team/aws/ssm", "v")
		h.put("my-awsome", "v")
	})
}

// The trimmed form is the one stored and echoed (UNVERIFIED SSM-04,
// ssm-parameters.md 3.1).
func TestTrimmedSpellingIsEchoed(t *testing.T) {
	run(t, func(h *harness) {
		h.put(" /echo/a  ", "v")
		if got := h.mustGet("  /echo/a", false); val(got.Name) != "/echo/a" {
			h.Errorf("Name = %q, want /echo/a", val(got.Name))
		}
		if got := h.mustDescribeOne("/echo/a "); val(got.Name) != "/echo/a" {
			h.Errorf("DescribeParameters Name = %q, want /echo/a", val(got.Name))
		}
	})
}

// 16 levels are refused like 17 (UNVERIFIED SSM-05, ssm-parameters.md
// 3.4: the docs show only 15 accepted and 17 refused), and the refusal
// names the name and the limit.
func TestSixteenLevelsAreRefused(t *testing.T) {
	run(t, func(h *harness) {
		name := strings.Repeat("/l", 15) + "/leaf"
		_, err := call(h, ssmapi.OpPutParameter, &ssmapi.PutParameterInput{Name: name, Value: "secret-value", Type: new(ssmapi.ParameterTypeString)})
		wantCode(h.T, "PutParameter of 16 levels", err, ssmapi.IdentityHierarchyLevelLimitExceededException())
		wantOwnMessage(h.T, "PutParameter of 16 levels", err, "secret-value", name, "15", "16")
	})
}

// The name rules hold on reads and deletes too (ssm-parameters.md 3.1:
// the space text "appears on every read operation as well"), and a
// ResourceId that is not a valid name names no parameter: InvalidResourceId
// (contract 0).
func TestNameRulesOnEveryOperation(t *testing.T) {
	run(t, func(h *harness) {
		wantCode(h.T, "GetParameter", h.get("bad name"), ssmapi.IdentityValidationException())
		_, err := call(h, ssmapi.OpDeleteParameter, &ssmapi.DeleteParameterInput{Name: "/a//b"})
		wantCode(h.T, "DeleteParameter", err, ssmapi.IdentityValidationException())
		_, err = call(h, ssmapi.OpListTagsForResource, &ssmapi.ListTagsForResourceInput{ResourceType: ssmapi.ResourceTypeForTaggingParameter, ResourceId: "a/b"})
		wantCode(h.T, "ListTagsForResource", err, ssmapi.IdentityInvalidResourceId())
	})
}

// A parameter ARN of another account or Region names nothing this call can
// see: ParameterNotFound with GetParameter's empty message (UNVERIFIED
// SSM-29, ssm-parameters.md 3.3). An ARN of another service or SSM resource
// type, or a malformed one, is SSM's ValidationException.
func TestForeignAndMalformedARNs(t *testing.T) {
	run(t, func(h *harness) {
		h.put("/arn/a", "v")
		for _, arn := range []string{
			"arn:aws:ssm:us-east-1:111111111111:parameter/arn/a",
			"arn:aws:ssm:eu-west-1:000000000000:parameter/arn/a",
		} {
			wantError(h.T, "GetParameter "+arn, h.get(arn), ssmapi.IdentityParameterNotFound(), "")
		}
		for _, arn := range []string{
			"arn:aws:ssm:us-east-1:000000000000:document/arn/a",
			"arn:aws:sqs:us-east-1:000000000000:parameter/arn/a",
			"arn:aws:ssm:us-east-1",
		} {
			wantCode(h.T, "GetParameter "+arn, h.get(arn), ssmapi.IdentityValidationException())
		}
		// The name an ARN carries follows the name rules.
		wantCode(h.T, "GetParameter by ARN of a bad name", h.get(arnPrefix+"a//b"), ssmapi.IdentityValidationException())
	})
}

// The reserved namespaces answer 501 at their own root as well (a path
// /aws/service lists public parameters in AWS, ssm-parameters.md 8.1),
// while a name merely starting with the same letters under another first
// level is an ordinary name.
func TestReservedNamespaceRoots(t *testing.T) {
	run(t, func(h *harness) {
		wantNotImplemented(h.T, "GetParameter /aws/service", h.get("/aws/service"), "/aws/service/")
		_, err := call(h, ssmapi.OpDescribeParameters, &ssmapi.DescribeParametersInput{
			ParameterFilters: []ssmapi.ParameterStringFilter{{Key: "Path", Values: []string{"/aws/reference/secretsmanager"}}},
		})
		wantNotImplemented(h.T, "DescribeParameters Path /aws/reference/secretsmanager", err, "/aws/reference/secretsmanager/")
		h.put("/x/aws/service/y", "v")
	})
}
