package iamapi

import (
	"strconv"
	"testing"

	"github.com/sustinbebustin/understudy/internal/awserr"
)

// TestTooManyTagsAreReportedWithoutAValue: AWS reports a list of structures
// over its length limit with no value (testdata/parity/iam/R15/06-tag-role-51.json).
func TestTooManyTagsAreReportedWithoutAValue(t *testing.T) {
	in := &TagRoleInput{RoleName: "role"}
	for i := 1; i <= 51; i++ {
		in.Tags = append(in.Tags, Tag{Key: "k" + strconv.Itoa(i), Value: "v"})
	}
	const want = "1 validation error detected: Value at 'tags' failed to satisfy constraint: Member must have length less than or equal to 50"
	if got := awserr.CoralMessage(in.Validate()); got != want {
		t.Fatalf("message = %s\nwant      %s", got, want)
	}
}
