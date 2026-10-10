package lambdaapi

import (
	"testing"

	"github.com/sustinbebustin/understudy/internal/awserr"
)

// TestRolledUpViolationReportsTheCollection: Lambda reports a list element's
// own constraints once, at the list, with the whole list as the value and
// the element constraints nested (error-matrix.md 4, style B, recorded for
// vpcConfig.subnetIds).
func TestRolledUpViolationReportsTheCollection(t *testing.T) {
	in := &CreateFunctionInput{
		FunctionName: "fn",
		Role:         "arn:aws:iam::123456789012:role/lambda-role",
		VpcConfig:    &VpcConfig{SubnetIds: []string{"subnet-1", "bad!"}},
	}
	vs := in.Validate()
	const want = "1 validation error detected: Value '[subnet-1, bad!]' at 'vpcConfig.subnetIds' failed to satisfy constraint: " +
		"Member must satisfy constraint: [Member must have length less than or equal to 1024, " +
		"Member must have length greater than or equal to 0, Member must satisfy regular expression pattern: subnet-[0-9a-z]*]"
	if got := awserr.CoralMessage(vs); got != want {
		t.Fatalf("message = %s\nwant      %s", got, want)
	}
	err := in.ValidationError(vs)
	if err == nil || err.ID.Shape != "com.amazon.coral.validate#ValidationException" || err.Message != want {
		t.Fatalf("ValidationError = %+v, want com.amazon.coral.validate#ValidationException with the message above", err)
	}
}
