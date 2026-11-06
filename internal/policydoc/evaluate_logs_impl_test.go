package policydoc_test

import (
	"testing"

	"github.com/sustinbebustin/understudy/internal/policydoc"
)

// A log group is authorized on its group ARN, and a statement on either
// the group ARN or its ":*" form allows the group-level actions: a role
// holding logs:* on log-group:<name>:* alone was allowed CreateLogGroup,
// DescribeLogStreams, PutRetentionPolicy, FilterLogEvents and
// DeleteLogGroup, and one holding it on log-group:<name> alone was allowed
// those and refused CreateLogStream and PutLogEvents on the group's stream
// (secrets-logs-events-alarms.md 3.10, Q14; testdata/parity/logs/IAM-ENF/42-*,
// IAM-ENF-42, aws-recorded).
func TestEvaluateLogGroupMatchesItsWildcardForm(t *testing.T) {
	const (
		group  = "arn:aws:logs:us-east-2:123456789012:log-group:/us1103/r28"
		stream = group + ":log-stream:s"
	)
	allows := func(pattern, action, resource string) bool {
		doc := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"logs:*","Resource":"` + pattern + `"}]}`
		r := policydoc.Evaluate(parseDoc(t, doc), policydoc.IdentityPolicy, policydoc.Request{Requester: evalUser, Action: action, Resource: resource})
		return len(r.Allows) == 1
	}
	cases := []struct {
		step, pattern, action, resource string
		want                            bool
	}{
		{"42-wild-create-group", group + ":*", "logs:CreateLogGroup", group, true},
		{"42-wild-describe-streams", group + ":*", "logs:DescribeLogStreams", group, true},
		{"42-wild-retention", group + ":*", "logs:PutRetentionPolicy", group, true},
		{"42-wild-filter", group + ":*", "logs:FilterLogEvents", group, true},
		{"42-wild-create-stream", group + ":*", "logs:CreateLogStream", stream, true},
		{"42-wild-put-events", group + ":*", "logs:PutLogEvents", stream, true},
		{"42-wild-delete-group", group + ":*", "logs:DeleteLogGroup", group, true},
		{"42-bare-create-group", group, "logs:CreateLogGroup", group, true},
		{"42-bare-describe-streams", group, "logs:DescribeLogStreams", group, true},
		{"42-bare-create-stream", group, "logs:CreateLogStream", stream, false},
		{"42-bare-put-events", group, "logs:PutLogEvents", stream, false},
		// Another group's ":*" form does not reach this one (inferred).
		{"another group", group + "-other:*", "logs:CreateLogGroup", group, false},
	}
	for _, c := range cases {
		if got := allows(c.pattern, c.action, c.resource); got != c.want {
			t.Errorf("%s: %s on %s under %s allowed = %t, want %t", c.step, c.action, c.resource, c.pattern, got, c.want)
		}
	}
}
